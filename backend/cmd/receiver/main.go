// Command receiver is a local webhook target for demos and tests. Its
// behaviour is chosen by URL path, it verifies EventMesh signatures, and it
// records every request so tests can inspect what was actually delivered.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"eventmesh/internal/webhook"
)

const maxRecorded = 20000

// Recorded is one received request, as returned by /control/requests.
type Recorded struct {
	At                time.Time         `json:"at"`
	Hook              string            `json:"hook"`
	Path              string            `json:"path"`
	DeliveryID        string            `json:"delivery_id"`
	EventID           string            `json:"event_id"`
	Attempt           string            `json:"attempt"`
	Status            int               `json:"status_returned"`
	SignatureVerified bool              `json:"signature_verified"`
	SignatureError    string            `json:"signature_error,omitempty"`
	Headers           map[string]string `json:"headers"`
	Body              json.RawMessage   `json:"body"`
}

type server struct {
	defaultSecret string

	mu       sync.Mutex
	switches map[string]string // hook name -> mode
	seen     map[string]int    // delivery id -> requests seen, for /flaky
	recorded []Recorded
}

func main() {
	addr := getenv("EM_RECEIVER_ADDR", "127.0.0.1:9000")
	s := &server{
		defaultSecret: os.Getenv("EM_RECEIVER_SECRET"),
		switches:      map[string]string{}, seen: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/{kind}", s.hook)
	mux.HandleFunc("POST /hooks/switch/{name}", s.hook)
	mux.HandleFunc("PUT /control/switch/{name}", s.setSwitch)
	mux.HandleFunc("GET /control/requests", s.requests)
	mux.HandleFunc("DELETE /control/requests", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.recorded, s.seen = nil, map[string]int{}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })

	log.Printf("receiver listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (s *server) secretFor(hook string) string {
	if v := os.Getenv("EM_RECEIVER_SECRET_" + hook); v != "" {
		return v
	}
	return s.defaultSecret
}

func (s *server) hook(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	name := r.PathValue("name")
	hookName := kind
	if name != "" {
		kind, hookName = "switch", name
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	deliveryID := r.Header.Get(webhook.HeaderDeliveryID)

	rec := Recorded{
		At: time.Now().UTC(), Hook: hookName, Path: r.URL.RequestURI(), DeliveryID: deliveryID,
		EventID: r.Header.Get(webhook.HeaderEventID), Attempt: r.Header.Get(webhook.HeaderAttempt),
		Headers: map[string]string{},
	}
	for k := range r.Header {
		rec.Headers[k] = r.Header.Get(k)
	}
	if json.Valid(body) {
		rec.Body = body
	}
	if secret := s.secretFor(hookName); secret == "" {
		rec.SignatureError = "no receiver secret configured"
	} else if err := webhook.Verify(secret, r.Header.Get(webhook.HeaderSignature),
		r.Header.Get(webhook.HeaderTimestamp), body, time.Now(), 5*time.Minute); err != nil {
		rec.SignatureError = err.Error()
	} else {
		rec.SignatureVerified = true
	}

	mode := kind
	if kind == "switch" {
		s.mu.Lock()
		mode = s.switches[hookName]
		s.mu.Unlock()
		if mode == "" {
			mode = "ok"
		}
	}
	rec.Status = s.respond(w, r, mode, deliveryID)
	s.mu.Lock()
	s.recorded = append(s.recorded, rec)
	if len(s.recorded) > maxRecorded {
		s.recorded = s.recorded[len(s.recorded)-maxRecorded:]
	}
	s.mu.Unlock()
}

// respond writes the response for a mode and returns the status it sent
// (0 when the connection was dropped).
func (s *server) respond(w http.ResponseWriter, r *http.Request, mode, deliveryID string) int {
	switch mode {
	case "fail":
		w.WriteHeader(http.StatusInternalServerError)
		return http.StatusInternalServerError
	case "client-error":
		w.WriteHeader(http.StatusBadRequest)
		return http.StatusBadRequest
	case "rate-limited":
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return http.StatusTooManyRequests
	case "slow":
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
	case "flaky":
		failFirst, _ := strconv.Atoi(r.URL.Query().Get("fail_first"))
		s.mu.Lock()
		s.seen[deliveryID]++
		n := s.seen[deliveryID]
		s.mu.Unlock()
		if n <= failFirst {
			w.WriteHeader(http.StatusInternalServerError)
			return http.StatusInternalServerError
		}
	case "drop":
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return 0
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
	return http.StatusOK
}

func (s *server) setSwitch(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		var in struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
		mode = in.Mode
	}
	switch mode {
	case "ok", "fail", "client-error", "rate-limited", "drop":
	default:
		http.Error(w, "mode must be one of ok, fail, client-error, rate-limited, drop", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.switches[r.PathValue("name")] = mode
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) requests(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("delivery_id")
	s.mu.Lock()
	out := make([]Recorded, 0)
	for _, rec := range s.recorded {
		if want == "" || rec.DeliveryID == want {
			out = append(out, rec)
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var _ = fmt.Sprint
