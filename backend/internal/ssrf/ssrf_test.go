package ssrf

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", // loopback
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", // private
		"169.254.169.254", "169.254.0.1", // link-local, cloud metadata
		"100.64.0.1", "100.127.255.255", // CGNAT
		"0.0.0.0", "0.1.2.3", // unspecified / this network
		"224.0.0.1", "239.255.255.255", // multicast
		"240.0.0.1", "255.255.255.255", // reserved, broadcast
		"192.0.2.10", "198.51.100.10", "203.0.113.10", // documentation
		"198.18.0.1", "192.0.0.1",
		"::1", "::", // loopback, unspecified
		"fc00::1", "fd00:ec2::254", // unique local, AWS IPv6 metadata
		"fe80::1", "fec0::1", // link-local, site-local
		"ff02::1", "ff05::2", // multicast
		"2001:db8::1", "2001::1", "2002:0a00:0001::1", "64:ff9b::a00:1", "100::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.0.1", // IPv4-mapped
	}
	for _, ip := range blocked {
		t.Run("blocked "+ip, func(t *testing.T) {
			err := CheckIP(netip.MustParseAddr(ip))
			assert.ErrorIs(t, err, ErrBlocked)
		})
	}

	allowed := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "100.128.0.1", "169.253.255.255",
		"2606:4700:4700::1111", "2a00:1450:4001:81b::200e", "::ffff:8.8.8.8",
	}
	for _, ip := range allowed {
		t.Run("allowed "+ip, func(t *testing.T) {
			assert.NoError(t, CheckIP(netip.MustParseAddr(ip)))
		})
	}

	t.Run("zero value", func(t *testing.T) {
		assert.ErrorIs(t, CheckIP(netip.Addr{}), ErrBlocked)
	})
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return addrs, nil
}

func TestValidateURL(t *testing.T) {
	resolver := fakeResolver{
		"public.example":   {netip.MustParseAddr("93.184.216.34")},
		"internal.example": {netip.MustParseAddr("10.1.2.3")},
		// One good and one bad record: must be rejected, the dialer could pick either.
		"mixed.example": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
		"receiver":      {netip.MustParseAddr("172.18.0.5")},
	}
	strict := NewPolicy(nil, resolver)
	withAllowlist := NewPolicy([]string{"Receiver"}, resolver)
	ctx := context.Background()

	cases := []struct {
		name    string
		policy  *Policy
		url     string
		wantErr bool
		blocked bool
	}{
		{"public https", strict, "https://public.example/hook", false, false},
		{"public http with port", strict, "http://public.example:8080/hook?x=1", false, false},
		{"public ip literal", strict, "https://93.184.216.34/hook", false, false},
		{"ftp scheme", strict, "ftp://public.example/hook", true, false},
		{"file scheme", strict, "file:///etc/passwd", true, false},
		{"no scheme", strict, "public.example/hook", true, false},
		{"credentials", strict, "https://user:pass@public.example/hook", true, false},
		{"username only", strict, "https://user@public.example/hook", true, false},
		{"empty host", strict, "https:///hook", true, false},
		{"fragment", strict, "https://public.example/hook#frag", true, false},
		{"resolves private", strict, "https://internal.example/hook", true, true},
		{"mixed records", strict, "https://mixed.example/hook", true, true},
		{"loopback literal", strict, "http://127.0.0.1:9000/hook", true, true},
		{"ipv6 loopback literal", strict, "http://[::1]:9000/hook", true, true},
		{"mapped loopback literal", strict, "http://[::ffff:127.0.0.1]/hook", true, true},
		{"metadata literal", strict, "http://169.254.169.254/latest/meta-data", true, true},
		{"unresolvable", strict, "https://nope.example/hook", true, false},
		{"private host without allowlist", strict, "http://receiver:9000/hooks/ok", true, true},
		{"private host with allowlist", withAllowlist, "http://receiver:9000/hooks/ok", false, false},
		{"allowlist is exact, not suffix", withAllowlist, "http://evil-receiver:9000/hooks/ok", true, false},
		{"allowlist does not exempt others", withAllowlist, "https://internal.example/hook", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.ValidateURL(ctx, tc.url)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.blocked, errors.Is(err, ErrBlocked), "ErrBlocked mismatch: %v", err)
		})
	}
}

// The connection-time check is the real control. This test dials a real
// loopback server: a strict policy must refuse at the socket, an allowlisted
// host must get through.
func TestDialContextEnforcesPolicyAtConnectionTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	newClient := func(p *Policy) *http.Client {
		tr := &http.Transport{DialContext: p.DialContext(&net.Dialer{Timeout: 2 * time.Second})}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr, Timeout: 5 * time.Second}
	}

	t.Run("blocked", func(t *testing.T) {
		resp, err := newClient(NewPolicy(nil, nil)).Get(srv.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlocked)
	})

	t.Run("allowlisted", func(t *testing.T) {
		resp, err := newClient(NewPolicy([]string{"127.0.0.1"}, nil)).Get(srv.URL)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("hostname resolving to loopback is blocked at dial", func(t *testing.T) {
		// "localhost" passes any string-based check but resolves to loopback;
		// only the Control hook, which sees the resolved IP, can stop it.
		_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
		require.NoError(t, err)
		resp, err := newClient(NewPolicy(nil, nil)).Get("http://localhost:" + port)
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlocked)
	})
}
