package common

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// SSRF protection for upstream relay calls and any server-side URL fetch.
// Private, loopback, link-local, multicast, unspecified, and cloud metadata
// addresses are rejected at both URL-validation and dial time (the dial-time
// check also defeats DNS rebinding).

// metadataIPv4 is the AWS/cloud metadata service address.
var metadataIPv4 = net.IPv4(169, 254, 169, 254)

// ssrfDisabled toggles SSRF protection off for local development (e.g. pointing
// a channel at a loopback mock upstream). Production must leave it enabled.
var ssrfDisabled = false

// InitSSRF applies SSRF environment configuration.
func InitSSRF() {
	ssrfDisabled = GetEnvBool("SSRF_DISABLE", false)
	if ssrfDisabled {
		Logger.Warn("SSRF protection disabled (SSRF_DISABLE=true) — for local development only")
	}
}

// SSRFDisabled reports whether SSRF protection is disabled.
func SSRFDisabled() bool { return ssrfDisabled }

// IsUnsafeIP reports whether an IP must not be contacted by the gateway.
func IsUnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ip.To4() != nil && ip.Equal(metadataIPv4) {
		return true
	}
	// IPv6 unique-local (fc00::/7) and link-local (fe80::/10) are already
	// covered by IsPrivate/IsLinkLocalUnicast in modern Go; belt-and-suspenders.
	if ip4 := ip.To4(); ip4 == nil && len(ip) == 16 {
		if ip[0]&0xfe == 0xfc || (ip[0] == 0xfe && ip[1]&0xc0 == 0x80) {
			return true
		}
	}
	return false
}

// ValidateURL parses and validates an outbound URL: http/https scheme only, a
// resolvable host, no embedded credentials, and no unsafe resolved addresses.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("ssrf: unsupported scheme")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("ssrf: missing host")
	}
	if u.User != nil {
		return errors.New("ssrf: embedded credentials not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("ssrf: cannot resolve host %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("ssrf: host %s resolves to no addresses", host)
	}
	for _, ip := range ips {
		if IsUnsafeIP(ip) {
			return fmt.Errorf("ssrf: unsafe address %s", ip.String())
		}
	}
	return nil
}

// SafeDialContext is a net.Dialer.DialContext replacement that rejects unsafe
// resolved addresses immediately before connecting (prevents DNS rebinding).
func SafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if ssrfDisabled {
		return dialer.DialContext(ctx, network, addr)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsUnsafeIP(ip) {
			return nil, fmt.Errorf("ssrf: blocked address %s", ip.String())
		}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			if IsUnsafeIP(a.IP) {
				return nil, fmt.Errorf("ssrf: blocked address %s", a.IP.String())
			}
		}
	}
	return dialer.DialContext(ctx, network, addr)
}
