// Command loadtest drives a running EventMesh stack: it creates its own user,
// API key and endpoints (pointing at the local receiver), submits events from
// concurrent clients, waits for every delivery to reach a terminal state and
// reports throughput and latency percentiles.
//
// Connection reuse: one http.Transport is shared by all clients. Opening a new
// TCP connection per request exhausts the Windows ephemeral port range
// (roughly 16k ports, each held in TIME_WAIT for about 2 minutes) within a few
// thousand requests, which would measure the OS instead of EventMesh.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type options struct {
	API, Receiver          string
	Events, Clients        int
	Healthy, Failing, Slow int
	SlowMS                 int
	Timeout                time.Duration
	Out                    string
	Label                  string
	EndpointRate           int
}

// Result is written to the JSON file so runs can be compared.
type Result struct {
	Label                string    `json:"label"`
	StartedAt            time.Time `json:"started_at"`
	GoVersion            string    `json:"go_version"`
	NumCPU               int       `json:"num_cpu"`
	Events               int       `json:"events"`
	Clients              int       `json:"clients"`
	Endpoints            int       `json:"endpoints"`
	Deliveries           int       `json:"deliveries"`
	SlowMS               int       `json:"slow_ms"`
	IngestRateLimited429 int64     `json:"ingest_429s"`
	IngestErrors         int64     `json:"ingest_errors"`
	IngestSeconds        float64   `json:"ingest_seconds"`
	IngestPerSecond      float64   `json:"ingest_events_per_second"`
	IngestP50MS          float64   `json:"ingest_p50_ms"`
	IngestP95MS          float64   `json:"ingest_p95_ms"`
	IngestP99MS          float64   `json:"ingest_p99_ms"`
	TotalSeconds         float64   `json:"total_seconds"`
	DeliveryPerSecond    float64   `json:"delivery_per_second"`
	E2EP50MS             float64   `json:"e2e_p50_ms"`
	E2EP95MS             float64   `json:"e2e_p95_ms"`
	E2EP99MS             float64   `json:"e2e_p99_ms"`
	Delivered            int       `json:"delivered"`
	DeadLetter           int       `json:"dead_letter"`
	Cancelled            int       `json:"cancelled"`
	Retries              int       `json:"retries"`
	TimedOut             bool      `json:"timed_out"`
}

func main() {
	var o options
	flag.StringVar(&o.API, "api", env("EM_API_BASE_URL", "http://127.0.0.1:8090"), "API base URL")
	flag.StringVar(&o.Receiver, "receiver", "http://127.0.0.1:9000", "receiver base URL as seen by the worker")
	flag.IntVar(&o.Events, "events", 1000, "events to send")
	flag.IntVar(&o.Clients, "clients", 20, "concurrent ingestion clients")
	flag.IntVar(&o.Healthy, "healthy", 3, "healthy endpoints (always 200)")
	flag.IntVar(&o.Failing, "failing", 0, "always-failing endpoints (500)")
	flag.IntVar(&o.Slow, "slow", 0, "slow endpoints")
	flag.IntVar(&o.SlowMS, "slow-ms", 200, "response delay of slow endpoints")
	flag.IntVar(&o.EndpointRate, "endpoint-rate", 1000, "per-endpoint rate limit (1-1000)")
	flag.DurationVar(&o.Timeout, "timeout", 5*time.Minute, "how long to wait for every delivery to finish")
	flag.StringVar(&o.Out, "out", "", "write the JSON result to this file")
	flag.StringVar(&o.Label, "label", "", "label stored with the result")
	flag.Parse()

	res, err := run(context.Background(), o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
	printResult(res)
	if o.Out != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(o.Out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write result:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", o.Out)
	}
	if res.TimedOut {
		os.Exit(2)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type apiClient struct {
	base string
	http *http.Client
}

func (c *apiClient) do(ctx context.Context, method, path string, body any, headers map[string]string, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "loadtest")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode < 300 {
		_ = json.Unmarshal(raw, out)
	}
	if res.StatusCode >= 400 && res.StatusCode != http.StatusTooManyRequests {
		return res.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, string(raw))
	}
	return res.StatusCode, nil
}

func run(ctx context.Context, o options) (Result, error) {
	transport := &http.Transport{
		MaxIdleConns: o.Clients * 2, MaxIdleConnsPerHost: o.Clients * 2, MaxConnsPerHost: o.Clients * 2,
		IdleConnTimeout: 90 * time.Second,
	}
	jar, _ := cookiejar.New(nil)
	session := &apiClient{base: o.API, http: &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second}}
	ingest := &apiClient{base: o.API, http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}

	email := fmt.Sprintf("loadtest-%d@example.com", time.Now().UnixNano())
	if _, err := session.do(ctx, "POST", "/auth/register", map[string]string{"email": email, "password": "loadtest-password-1"}, nil, nil); err != nil {
		return Result{}, fmt.Errorf("register: %w", err)
	}
	var key struct {
		Key string `json:"key"`
	}
	if _, err := session.do(ctx, "POST", "/api-keys", map[string]string{"name": "loadtest"}, nil, &key); err != nil {
		return Result{}, fmt.Errorf("create key: %w", err)
	}

	endpoints := 0
	create := func(name, path string) error {
		endpoints++
		_, err := session.do(ctx, "POST", "/endpoints", map[string]any{
			"name": name, "url": o.Receiver + path, "event_types": []string{"*"}, "rate_limit_per_second": o.EndpointRate,
		}, nil, nil)
		return err
	}
	for i := 0; i < o.Healthy; i++ {
		if err := create("healthy-"+strconv.Itoa(i), "/hooks/ok"); err != nil {
			return Result{}, err
		}
	}
	for i := 0; i < o.Failing; i++ {
		if err := create("failing-"+strconv.Itoa(i), "/hooks/fail"); err != nil {
			return Result{}, err
		}
	}
	for i := 0; i < o.Slow; i++ {
		if err := create("slow-"+strconv.Itoa(i), "/hooks/slow?ms="+strconv.Itoa(o.SlowMS)); err != nil {
			return Result{}, err
		}
	}
	if endpoints == 0 {
		return Result{}, fmt.Errorf("no endpoints requested")
	}

	// Ingestion phase.
	res := Result{
		Label: o.Label, StartedAt: time.Now().UTC(), GoVersion: runtime.Version(), NumCPU: runtime.NumCPU(),
		Events: o.Events, Clients: o.Clients, Endpoints: endpoints, SlowMS: o.SlowMS,
	}
	jobs := make(chan int)
	var mu sync.Mutex
	var latencies []float64
	var limited, failed atomic.Int64
	start := time.Now()
	var wg sync.WaitGroup
	for c := 0; c < o.Clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				for {
					t0 := time.Now()
					code, err := ingest.do(ctx, "POST", "/events",
						map[string]any{"event_type": "load.test", "payload": map[string]any{"n": n}},
						map[string]string{"Authorization": "Bearer " + key.Key}, nil)
					if err == nil && code == http.StatusTooManyRequests {
						limited.Add(1)
						time.Sleep(100 * time.Millisecond)
						continue
					}
					if err != nil {
						failed.Add(1)
						break
					}
					mu.Lock()
					latencies = append(latencies, float64(time.Since(t0).Microseconds())/1000)
					mu.Unlock()
					break
				}
			}
		}()
	}
	for n := 0; n < o.Events; n++ {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
	ingestDur := time.Since(start)
	res.IngestSeconds = ingestDur.Seconds()
	res.IngestRateLimited429, res.IngestErrors = limited.Load(), failed.Load()
	accepted := len(latencies)
	res.IngestPerSecond = float64(accepted) / ingestDur.Seconds()
	res.IngestP50MS, res.IngestP95MS, res.IngestP99MS = pct(latencies, 50), pct(latencies, 95), pct(latencies, 99)
	res.Deliveries = accepted * endpoints

	// Wait until every delivery is terminal.
	deadline := time.Now().Add(o.Timeout)
	for {
		var stats struct {
			Totals struct {
				Deliveries int `json:"deliveries"`
				Delivered  int `json:"delivered"`
				DeadLetter int `json:"dead_letter"`
				Cancelled  int `json:"cancelled"`
			} `json:"totals"`
		}
		if _, err := session.do(ctx, "GET", "/dashboard/stats?range=24h", nil, nil, &stats); err != nil {
			return res, err
		}
		t := stats.Totals
		res.Delivered, res.DeadLetter, res.Cancelled = t.Delivered, t.DeadLetter, t.Cancelled
		if t.Delivered+t.DeadLetter+t.Cancelled >= res.Deliveries {
			break
		}
		if time.Now().After(deadline) {
			res.TimedOut = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	total := time.Since(start)
	res.TotalSeconds = total.Seconds()
	res.DeliveryPerSecond = float64(res.Delivered+res.DeadLetter) / total.Seconds()

	// Per-delivery latency and retry counts from the delivery list.
	var e2e []float64
	cursor := ""
	for {
		var page struct {
			Items []struct {
				Status        string     `json:"status"`
				AttemptCount  int        `json:"attempt_count"`
				CreatedAt     time.Time  `json:"created_at"`
				LastAttemptAt *time.Time `json:"last_attempt_at"`
			} `json:"items"`
			Next *string `json:"next_cursor"`
		}
		path := "/deliveries?limit=100"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		if _, err := session.do(ctx, "GET", path, nil, nil, &page); err != nil {
			return res, err
		}
		for _, d := range page.Items {
			if d.AttemptCount > 1 {
				res.Retries += d.AttemptCount - 1
			}
			if d.Status == "delivered" && d.LastAttemptAt != nil {
				e2e = append(e2e, float64(d.LastAttemptAt.Sub(d.CreatedAt).Microseconds())/1000)
			}
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		cursor = *page.Next
	}
	res.E2EP50MS, res.E2EP95MS, res.E2EP99MS = pct(e2e, 50), pct(e2e, 95), pct(e2e, 99)
	return res, nil
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	idx := int(float64(len(s)-1) * p / 100)
	return s[idx]
}

func printResult(r Result) {
	fmt.Printf("\nEventMesh load test %s\n", r.Label)
	fmt.Printf("  events %d, clients %d, endpoints %d, deliveries %d\n", r.Events, r.Clients, r.Endpoints, r.Deliveries)
	fmt.Printf("  ingestion: %.0f events/s over %.2fs (429s %d, errors %d)\n", r.IngestPerSecond, r.IngestSeconds, r.IngestRateLimited429, r.IngestErrors)
	fmt.Printf("  ingestion latency ms: p50 %.1f  p95 %.1f  p99 %.1f\n", r.IngestP50MS, r.IngestP95MS, r.IngestP99MS)
	fmt.Printf("  delivery: %.0f/s, total %.2fs; delivered %d, dead letter %d, cancelled %d, retries %d\n",
		r.DeliveryPerSecond, r.TotalSeconds, r.Delivered, r.DeadLetter, r.Cancelled, r.Retries)
	fmt.Printf("  end-to-end latency ms (delivered): p50 %.1f  p95 %.1f  p99 %.1f\n", r.E2EP50MS, r.E2EP95MS, r.E2EP99MS)
	if r.TimedOut {
		fmt.Println("  TIMED OUT waiting for deliveries to finish")
	}
}
