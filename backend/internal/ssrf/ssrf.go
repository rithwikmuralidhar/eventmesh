// Package ssrf is the single place that decides whether EventMesh may connect
// to a user-supplied webhook destination. Endpoint validation uses it for
// early feedback; the worker's HTTP client uses it at connection time, which
// is the check that actually counts.
package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// ErrBlocked marks a destination rejected by the policy. Callers test for it
// with errors.Is to classify the failure as non-retryable.
var ErrBlocked = errors.New("destination blocked by SSRF policy")

// blockedPrefixes lists reserved ranges that the netip helper methods
// (IsLoopback, IsPrivate, ...) do not cover.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, includes broadcast
	"64:ff9b::/96",    // NAT64: embeds an IPv4 address
	"64:ff9b:1::/48",  // local-use NAT64
	"100::/64",        // discard-only
	"2001::/23",       // IETF protocol assignments, includes Teredo
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4: embeds an IPv4 address
	"fec0::/10",       // deprecated site-local
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// Resolver is the part of net.Resolver the policy needs. It is an interface
// so validation can be tested without real DNS.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy decides which destinations are allowed.
type Policy struct {
	allowedHosts map[string]struct{}
	resolver     Resolver
}

// NewPolicy builds a policy. allowedHosts are hostnames (or IP literals)
// exempt from the IP checks; it exists only so local development and tests
// can reach a receiver on a private network. A nil resolver uses the system one.
func NewPolicy(allowedHosts []string, resolver Resolver) *Policy {
	hosts := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		h = normalizeHost(h)
		if h != "" {
			hosts[h] = struct{}{}
		}
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Policy{allowedHosts: hosts, resolver: resolver}
}

// AllowedHosts returns the exempt hosts, for logging at startup.
func (p *Policy) AllowedHosts() []string {
	out := make([]string, 0, len(p.allowedHosts))
	for h := range p.allowedHosts {
		out = append(out, h)
	}
	return out
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

func (p *Policy) hostAllowed(host string) bool {
	_, ok := p.allowedHosts[normalizeHost(host)]
	return ok
}

// CheckIP returns ErrBlocked if addr is in a range EventMesh must not call.
func CheckIP(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}
	// An IPv4-mapped IPv6 address (::ffff:10.0.0.1) reaches the IPv4 host, so
	// it must be judged as that IPv4 address.
	addr = addr.Unmap().WithZone("")

	switch {
	case addr.IsUnspecified(),
		addr.IsLoopback(),
		addr.IsPrivate(),          // 10/8, 172.16/12, 192.168/16, fc00::/7
		addr.IsLinkLocalUnicast(), // 169.254/16 (cloud metadata lives here), fe80::/10
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast():
		return fmt.Errorf("%w: %s is in a reserved range", ErrBlocked, addr)
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: %s is in reserved range %s", ErrBlocked, addr, prefix)
		}
	}
	return nil
}

// ParseURL checks the parts of a webhook URL that do not need DNS: scheme,
// credentials and host. It returns the parsed URL.
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("url scheme must be http or https")
	}
	if u.User != nil {
		return nil, errors.New("url must not contain credentials")
	}
	if u.Hostname() == "" {
		return nil, errors.New("url must have a host")
	}
	if u.Fragment != "" {
		return nil, errors.New("url must not contain a fragment")
	}
	return u, nil
}

// ValidateURL is the early-feedback check run when an endpoint is saved. It
// resolves the host and rejects blocked addresses. It is a convenience only:
// DNS can change after this returns, so the binding check is DialControl.
func (p *Policy) ValidateURL(ctx context.Context, raw string) error {
	u, err := ParseURL(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if p.hostAllowed(host) {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return CheckIP(ip)
	}
	addrs, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("host %q could not be resolved", host)
	}
	// Every address must be allowed: the dialer may pick any of them.
	for _, a := range addrs {
		if err := CheckIP(a); err != nil {
			return err
		}
	}
	return nil
}

// DialControl is a net.Dialer.Control function. It runs after DNS resolution,
// immediately before the socket connects, and sees the real destination IP.
// That is what defeats DNS rebinding: whatever a hostname resolved to at
// validation time, the address checked here is the one actually used.
func DialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", ErrBlocked, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", ErrBlocked, address)
	}
	return CheckIP(ip)
}

// DialContext returns the dial function for the delivery HTTP transport.
// Allowlisted hostnames are dialed without the IP check; everything else goes
// through a dialer whose Control hook enforces CheckIP. The decision is made
// on the hostname the transport asks for, before resolution, because Control
// itself only ever sees an IP.
func (p *Policy) DialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	guarded := *base
	guarded.Control = DialControl
	exempt := *base
	exempt.Control = nil

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err == nil && p.hostAllowed(host) {
			return exempt.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
}
