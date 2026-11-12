package tests

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openStream connects to /stream as the client's user and returns a channel of
// the "data:" payload lines it receives.
func openStream(t *testing.T, c *client) <-chan string {
	t.Helper()
	req, err := http.NewRequest("GET", c.base+"/stream", nil)
	require.NoError(t, err)
	res, err := (&http.Client{Jar: c.http.Jar}).Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/event-stream")
	t.Cleanup(func() { _ = res.Body.Close() })

	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				lines <- strings.TrimSpace(data)
			}
		}
		close(lines)
	}()
	return lines
}

func waitFor(t *testing.T, ch <-chan string, contains string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-ch:
			require.True(t, ok, "stream closed while waiting for %q", contains)
			if strings.Contains(l, contains) {
				return
			}
		case <-deadline:
			t.Fatalf("no stream update containing %q", contains)
		}
	}
}

func TestStreamDeliversLiveUpdatesOnlyToTheOwner(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	a, b := s.newClient(), s.newClient()
	a.register("a@example.com")
	b.register("b@example.com")
	a.createEndpoint("hook", tg.URL, "*")
	keyA := a.createKey("k")

	streamA, streamB := openStream(t, a), openStream(t, b)
	time.Sleep(300 * time.Millisecond) // let both subscriptions register
	s.startWorker("w", 2, nil)
	s.ingest(keyA, "x.y", map[string]any{})

	waitFor(t, streamA, "event.created")
	waitFor(t, streamA, `"status":"delivered"`)

	select {
	case l := <-streamB:
		t.Fatalf("user B received user A's update: %s", l)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestAuditLogsAreScopedToTheUser(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	a, b := s.newClient(), s.newClient()
	a.register("a@example.com")
	b.register("b@example.com")
	endpointID, _ := a.createEndpoint("hook", tg.URL, "*")

	ra := a.do("GET", "/audit-logs", nil)
	require.Equal(t, http.StatusOK, ra.Status)
	assert.Contains(t, string(ra.Raw), endpointID, "owner sees the endpoint creation")
	rb := b.do("GET", "/audit-logs", nil)
	require.Equal(t, http.StatusOK, rb.Status)
	assert.NotContains(t, string(rb.Raw), endpointID, "other users never see it")
}

func TestUnauthenticatedRequestsToManagementRoutesAreRejected(t *testing.T) {
	s := newStack(t, nil)
	anon := s.newClient()
	for _, path := range []string{"/endpoints", "/events", "/deliveries", "/api-keys", "/audit-logs", "/dashboard/stats", "/settings", "/stream", "/auth/me"} {
		assert.Equal(t, http.StatusUnauthorized, anon.do("GET", path, nil).Status, path)
	}
}

func TestCSRFOriginCheckAllowsSameOriginAndRejectsOthers(t *testing.T) {
	s := newStack(t, nil)
	c := s.newClient()
	creds := map[string]string{"email": "a@example.com", "password": "correct-horse-1"}

	evil := c.do("POST", "/auth/register", creds, "Origin", "http://evil.example")
	assert.Equal(t, http.StatusForbidden, evil.Status, "foreign origin")

	own := c.do("POST", "/auth/register", creds, "Origin", s.api.URL)
	assert.Equal(t, http.StatusCreated, own.Status, "the API's own origin (frontend served by the API): %s", own.Raw)
}
