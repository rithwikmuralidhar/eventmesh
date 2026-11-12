package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/audit"
	"eventmesh/internal/clock"
	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
	"eventmesh/internal/token"
	"eventmesh/internal/users"
)

// sessionTouchInterval limits how often an active session writes its
// last_seen_at and sliding expiry, so authenticated reads do not each cost a write.
const sessionTouchInterval = time.Minute

// errInvalidCredentials is deliberately the same for "unknown email" and
// "wrong password": different answers would let anyone test which emails
// have accounts.
var errInvalidCredentials = httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")

// Session is an authenticated dashboard session.
type Session struct {
	ID   uuid.UUID
	User users.User
}

// Service implements registration, login and server-side sessions.
type Service struct {
	pool       *pgxpool.Pool
	queries    *db.Queries
	hasher     PasswordHasher
	clock      clock.Clock
	sessionTTL time.Duration
	// dummyHash is verified when a login names an unknown email, so that
	// request takes about as long as one for a real account.
	dummyHash string
}

// NewService builds the auth service.
func NewService(pool *pgxpool.Pool, hasher PasswordHasher, clk clock.Clock, sessionTTL time.Duration) (*Service, error) {
	dummy, err := hasher.Hash("eventmesh-timing-equalizer")
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Service{
		pool: pool, queries: db.New(pool), hasher: hasher, clock: clk,
		sessionTTL: sessionTTL, dummyHash: dummy,
	}, nil
}

// SessionTTL returns the session lifetime, used for the cookie's Max-Age.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// Register creates an account and its first session, returning the session token.
func (s *Service) Register(ctx context.Context, email, password string) (users.User, string, error) {
	email = users.NormalizeEmail(email)
	fields := map[string]string{}
	if err := users.ValidateEmail(email); err != nil {
		fields["email"] = err.Error()
	}
	if err := ValidatePassword(password); err != nil {
		fields["password"] = err.Error()
	}
	if len(fields) > 0 {
		return users.User{}, "", httpx.Validation(fields)
	}

	hash, err := s.hasher.Hash(password)
	if err != nil {
		return users.User{}, "", fmt.Errorf("hash password: %w", err)
	}
	userID, err := uuid.NewV7()
	if err != nil {
		return users.User{}, "", fmt.Errorf("generate user id: %w", err)
	}

	var user users.User
	var sessionToken string
	err = database.InTx(ctx, s.pool, func(q *db.Queries) error {
		row, err := q.CreateUser(ctx, db.CreateUserParams{ID: userID, Email: email, PasswordHash: hash})
		if err != nil {
			if isUniqueViolation(err) {
				return httpx.Conflict("email_taken", "An account with this email already exists.")
			}
			return fmt.Errorf("create user: %w", err)
		}
		user = users.User{ID: row.ID, Email: row.Email, CreatedAt: row.CreatedAt}
		if err := audit.Record(ctx, q, audit.Entry{
			UserID: row.ID, Action: audit.ActionUserRegistered, ResourceType: audit.ResourceUser, ResourceID: row.ID,
		}); err != nil {
			return err
		}
		sessionToken, err = s.createSession(ctx, q, row.ID)
		return err
	})
	if err != nil {
		return users.User{}, "", err
	}
	return user, sessionToken, nil
}

// Login checks credentials and starts a session.
func (s *Service) Login(ctx context.Context, email, password string) (users.User, string, error) {
	email = users.NormalizeEmail(email)
	row, err := s.queries.GetUserByEmail(ctx, email)
	if database.IsNotFound(err) {
		// Burn the same CPU a real verification would.
		_, _ = VerifyPassword(password, s.dummyHash)
		return users.User{}, "", errInvalidCredentials
	}
	if err != nil {
		return users.User{}, "", fmt.Errorf("load user: %w", err)
	}
	ok, err := VerifyPassword(password, row.PasswordHash)
	if err != nil {
		return users.User{}, "", fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return users.User{}, "", errInvalidCredentials
	}

	var sessionToken string
	err = database.InTx(ctx, s.pool, func(q *db.Queries) error {
		if err := q.DeleteExpiredSessions(ctx, row.ID); err != nil {
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		sessionToken, err = s.createSession(ctx, q, row.ID)
		return err
	})
	if err != nil {
		return users.User{}, "", err
	}
	return users.User{ID: row.ID, Email: row.Email, CreatedAt: row.CreatedAt}, sessionToken, nil
}

// createSession stores a new session and returns its token. Only the SHA-256
// of the token is stored, so a database leak does not yield usable sessions.
func (s *Service) createSession(ctx context.Context, q *db.Queries, userID uuid.UUID) (string, error) {
	raw, err := token.RandomBytes(32)
	if err != nil {
		return "", err
	}
	sessionToken := base64.RawURLEncoding.EncodeToString(raw)
	sessionID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	err = q.CreateSession(ctx, db.CreateSessionParams{
		ID:        sessionID,
		UserID:    userID,
		TokenHash: token.SHA256(sessionToken),
		ExpiresAt: s.clock.Now().Add(s.sessionTTL),
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if err := audit.Record(ctx, q, audit.Entry{
		UserID: userID, Action: audit.ActionSessionCreated, ResourceType: audit.ResourceSession, ResourceID: sessionID,
	}); err != nil {
		return "", err
	}
	return sessionToken, nil
}

// Authenticate resolves a session token. Unknown and expired tokens return
// ErrNoSession. An active session has its expiry slid forward.
func (s *Service) Authenticate(ctx context.Context, sessionToken string) (Session, error) {
	if sessionToken == "" {
		return Session{}, ErrNoSession
	}
	row, err := s.queries.GetSessionByTokenHash(ctx, token.SHA256(sessionToken))
	if database.IsNotFound(err) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	now := s.clock.Now()
	if now.Sub(row.LastSeenAt) >= sessionTouchInterval {
		err := s.queries.TouchSession(ctx, db.TouchSessionParams{ID: row.ID, ExpiresAt: now.Add(s.sessionTTL)})
		if err != nil {
			return Session{}, fmt.Errorf("touch session: %w", err)
		}
	}
	return Session{
		ID:   row.ID,
		User: users.User{ID: row.UserID, Email: row.Email, CreatedAt: row.UserCreatedAt},
	}, nil
}

// ErrNoSession means the request carries no valid session.
var ErrNoSession = errors.New("no valid session")

// Logout deletes the session row, which revokes it immediately.
func (s *Service) Logout(ctx context.Context, userID, sessionID uuid.UUID) error {
	return database.InTx(ctx, s.pool, func(q *db.Queries) error {
		if err := q.DeleteSession(ctx, db.DeleteSessionParams{ID: sessionID, UserID: userID}); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionSessionEnded, ResourceType: audit.ResourceSession, ResourceID: sessionID,
		})
	})
}

// ChangePassword verifies the current password, stores the new one and ends
// every other session of the user, so a stolen session stops working.
func (s *Service) ChangePassword(ctx context.Context, userID, sessionID uuid.UUID, current, next string) error {
	if err := ValidatePassword(next); err != nil {
		return httpx.Validation(map[string]string{"new_password": err.Error()})
	}
	row, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	ok, err := VerifyPassword(current, row.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return httpx.Validation(map[string]string{"current_password": "current password is incorrect"})
	}
	hash, err := s.hasher.Hash(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return database.InTx(ctx, s.pool, func(q *db.Queries) error {
		if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if err := q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: userID, ID: sessionID}); err != nil {
			return fmt.Errorf("delete other sessions: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionPasswordChanged, ResourceType: audit.ResourceUser, ResourceID: userID,
		})
	})
}

// EnsureUser creates the account if it does not exist and returns it. It is
// used by the seed and the load test, which must be safe to run repeatedly.
func (s *Service) EnsureUser(ctx context.Context, email, password string) (users.User, error) {
	email = users.NormalizeEmail(email)
	row, err := s.queries.GetUserByEmail(ctx, email)
	if err == nil {
		return users.User{ID: row.ID, Email: row.Email, CreatedAt: row.CreatedAt}, nil
	}
	if !database.IsNotFound(err) {
		return users.User{}, fmt.Errorf("load user: %w", err)
	}
	user, _, err := s.Register(ctx, email, password)
	return user, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
