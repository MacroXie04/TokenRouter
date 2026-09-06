package common

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	maxRedirectURLBytes     = 2048
	maxTrustedDomainBytes   = 253
	maxTrustedDomainEntries = 128
)

// ValidateRedirectURL checks that a redirect URL is http(s) and its domain is
// in the trusted-redirect-domain list (exact match or subdomain). The list is
// configured via TRUSTED_REDIRECT_DOMAINS (comma-separated); the server's own
// host is always trusted.
func ValidateRedirectURL(rawURL string) error {
	if !validRedirectText(rawURL, maxRedirectURLBytes) || rawURL != strings.TrimSpace(rawURL) ||
		strings.Contains(rawURL, `\`) {
		return fmt.Errorf("invalid URL format")
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL format")
	}
	if !parsedURL.IsAbs() || parsedURL.Opaque != "" || parsedURL.User != nil ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("invalid URL scheme: only http and https are allowed")
	}
	domain := normalizeTrustedDomain(parsedURL.Hostname())
	if domain == "" {
		return fmt.Errorf("invalid URL: missing host")
	}
	if parsedURL.Scheme == "http" {
		address := net.ParseIP(domain)
		if domain != "localhost" && !strings.HasSuffix(domain, ".localhost") &&
			(address == nil || !address.IsLoopback()) {
			return fmt.Errorf("invalid URL scheme: HTTPS is required for non-loopback redirects")
		}
	}
	for _, trustedDomain := range TrustedRedirectDomains() {
		trustedIP := net.ParseIP(trustedDomain)
		if domain == trustedDomain || trustedIP == nil && strings.HasSuffix(domain, "."+trustedDomain) {
			return nil
		}
	}
	return fmt.Errorf("domain %s is not in the trusted domains list", domain)
}

// TrustedRedirectDomains returns the trusted redirect domains: the
// configured TRUSTED_REDIRECT_DOMAINS list plus the server address host.
func TrustedRedirectDomains() []string {
	domains := []string{}
	seen := map[string]struct{}{}
	appendDomain := func(raw string) {
		if len(domains) >= maxTrustedDomainEntries {
			return
		}
		domain := normalizeTrustedDomain(raw)
		if domain == "" {
			return
		}
		if _, duplicate := seen[domain]; duplicate {
			return
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	if raw := GetEnv("TRUSTED_REDIRECT_DOMAINS", ""); raw != "" {
		for _, d := range strings.Split(raw, ",") {
			appendDomain(d)
		}
	}
	if server := GetEnv("SERVER_ADDRESS", ""); server != "" {
		if parsed, err := url.Parse(server); err == nil && parsed.Hostname() != "" {
			appendDomain(parsed.Hostname())
		}
	}
	return domains
}

func normalizeTrustedDomain(raw string) string {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if domain == "" || len(domain) > maxTrustedDomainBytes || !validRedirectText(domain, maxTrustedDomainBytes) {
		return ""
	}
	if address := net.ParseIP(domain); address != nil {
		return address.String()
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return ""
		}
	}
	return domain
}

func validRedirectText(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}
