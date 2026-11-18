package auth

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"eventmesh/internal/httpx"
	"eventmesh/internal/ratelimit"
	"eventmesh/internal/users"
)

// CookieName is the session cookie.
const CookieName = "eventmesh_session"

// Handler serves the /auth routes.
type Handler struct {
	service      *Service
	limiter      *ratelimit.Limiter
	cookieSecure bool
	// attemptsPerMinute bounds login and registration attempts, counted
	// separately per client IP and per email address.
	attemptsPerMinute int
	// registrationClosed refuses new accounts (EM_ALLOW_REGISTRATION=false),
	// for a public deployment that only its owner should use.
	registrationClosed bool
}

// NewHandler returns the auth handler. Registration is open.
func NewHandler(service *Service, limiter *ratelimit.Limiter, cookieSecure bool, attemptsPerMinute int) *Handler {
	return &Handler{service: service, limiter: limiter, cookieSecure: cookieSecure, attemptsPerMinute: attemptsPerMinute}
}

// WithRegistration opens or closes POST /auth/register. Existing accounts can
// always sign in.
func (h *Handler) WithRegistration(open bool) *Handler {
	h.registrationClosed = !open
	return h
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register handles POST /auth/register.
func (h *Handler) Register(c *gin.Context) {
	if h.registrationClosed {
		httpx.WriteError(c, httpx.NewError(http.StatusForbidden, "registration_closed", "Registration is closed on this server."))
		return
	}
	var req credentialsRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	if !h.allowAttempt(c, "register", req.Email) {
		return
	}
	user, sessionToken, err := h.service.Register(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	h.setCookie(c, sessionToken, int(h.service.SessionTTL().Seconds()))
	c.JSON(http.StatusCreated, gin.H{"user": user})
}

// Login handles POST /auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req credentialsRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	if !h.allowAttempt(c, "login", req.Email) {
		return
	}
	user, sessionToken, err := h.service.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	h.setCookie(c, sessionToken, int(h.service.SessionTTL().Seconds()))
	c.JSON(http.StatusOK, gin.H{"user": user})
}

// Logout handles POST /auth/logout.
func (h *Handler) Logout(c *gin.Context) {
	if err := h.service.Logout(c.Request.Context(), httpx.UserID(c), httpx.SessionID(c)); err != nil {
		httpx.WriteError(c, err)
		return
	}
	h.setCookie(c, "", -1)
	c.Status(http.StatusNoContent)
}

// Me handles GET /auth/me.
func (h *Handler) Me(c *gin.Context) {
	user, ok := c.Get(ctxUser)
	if !ok {
		httpx.WriteError(c, httpx.Unauthorized("Sign in to continue."))
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": user})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword handles POST /auth/change-password.
func (h *Handler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	err := h.service.ChangePassword(c.Request.Context(), httpx.UserID(c), httpx.SessionID(c), req.CurrentPassword, req.NewPassword)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

const ctxUser = "eventmesh.user"

// RequireSession is the middleware for dashboard routes. It resolves the
// session cookie to a user; the user ID used by every later query comes from
// here and never from the request.
func (h *Handler) RequireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionToken, err := c.Cookie(CookieName)
		if err != nil {
			httpx.WriteError(c, httpx.Unauthorized("Sign in to continue."))
			return
		}
		session, err := h.service.Authenticate(c.Request.Context(), sessionToken)
		if errors.Is(err, ErrNoSession) {
			httpx.WriteError(c, httpx.Unauthorized("Your session has expired. Sign in again."))
			return
		}
		if err != nil {
			httpx.WriteError(c, err)
			return
		}
		httpx.SetUser(c, session.User.ID)
		httpx.SetSessionID(c, session.ID)
		c.Set(ctxUser, session.User)
		c.Next()
	}
}

// setCookie writes the session cookie. HttpOnly keeps it away from scripts;
// SameSite=Lax stops it being sent on cross-site POSTs; Secure is on outside
// local development.
func (h *Handler) setCookie(c *gin.Context, value string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(CookieName, value, maxAge, "/", "", h.cookieSecure, true)
}

// allowAttempt applies the login/registration limit per IP and per email. It
// writes the 429 itself and returns false when the attempt is refused.
func (h *Handler) allowAttempt(c *gin.Context, kind, email string) bool {
	rate := float64(h.attemptsPerMinute) / 60
	keys := []string{ratelimit.AuthKey(kind+":ip", c.ClientIP())}
	if normalized := users.NormalizeEmail(email); normalized != "" && len(normalized) <= users.MaxEmailLen {
		keys = append(keys, ratelimit.AuthKey(kind+":email", normalized))
	}
	for _, key := range keys {
		decision := h.limiter.AllowOrFailOpen(c.Request.Context(), key, rate, h.attemptsPerMinute)
		if !decision.Allowed {
			c.Header("Retry-After", strconv.Itoa(int(math.Ceil(decision.RetryAfter.Seconds()))))
			httpx.WriteError(c, httpx.NewError(http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later."))
			return false
		}
	}
	return true
}
