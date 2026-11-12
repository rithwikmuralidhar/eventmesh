package tests

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nothing sensitive may reach the logs: not the endpoint secret, the API key,
// the session cookie, the password, the signature, or the event payload.
func TestLogsNeverContainSecretsOrPayloads(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("log-check@example.com")
	_, secret := c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")
	s.startWorker("w", 2, nil)

	const payloadMarker = "PAYLOAD-MARKER-9f3a1c"
	r := s.ingest(key, "x.y", map[string]any{"card": payloadMarker})
	require.Equal(t, http.StatusAccepted, r.Status)
	s.waitStatus("delivered", 1, 10*time.Second)

	// A failing delivery logs too.
	tg.setStatus(500)
	s.ingest(key, "x.y", map[string]any{"card": payloadMarker})
	s.waitStatus("dead_letter", 1, 15*time.Second)

	logs := s.logs.String()
	require.NotEmpty(t, logs, "the test must actually capture log output")
	require.Contains(t, logs, "delivery attempt finished", "worker logs were captured")
	for name, sensitive := range map[string]string{
		"endpoint secret": secret, "API key": key, "payload": payloadMarker,
		"password": "correct-horse-1", "signature prefix": "v1=",
	} {
		assert.NotContains(t, logs, sensitive, "%s leaked into the logs", name)
	}
}
