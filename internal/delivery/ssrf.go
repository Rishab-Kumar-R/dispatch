package delivery

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
)

var (
	ErrBlockedDestination = errors.New("delivery: destination address not allowed")
	ErrInvalidURL         = errors.New("delivery: invalid endpoint url")
)

// Ranges not covered by the netip helper methods: CGNAT, documentation,
// benchmarking, reserved, and IPv6 transition ranges that can embed IPv4.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// CheckAddr rejects any address that is not a public unicast address:
// loopback, RFC 1918, link-local (incl. the 169.254.169.254 metadata service),
// ULA (fc00::/7), multicast, unspecified, and the ranges above.
func CheckAddr(addr netip.Addr) error {
	addr = addr.Unmap() // ::ffff:10.0.0.1 is just 10.0.0.1

	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, addr)
	}

	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return fmt.Errorf("%w: %s", ErrBlockedDestination, addr)
		}
	}

	return nil
}

// ValidateEndpointURL is the early-feedback check used by the API when an
// endpoint is created. It cannot be the only defence (DNS can change after
// creation); the dial-time check in the client is the real one.
func ValidateEndpointURL(raw string) error {
	return validateURL(raw, CheckAddr)
}

func validateURL(raw string, check func(addr netip.Addr) error) error {
	if raw == "" || len(raw) > 2048 {
		return fmt.Errorf("%w: bad length", ErrInvalidURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme must be https", ErrInvalidURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in url not allowed", ErrInvalidURL)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%w: bad port", ErrInvalidURL)
		}
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if err := check(addr); err != nil {
			return err
		}
	}

	return nil
}
