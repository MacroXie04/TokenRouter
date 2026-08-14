package common

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateRedirectURL checks that a redirect URL is http(s) and its domain is
// in the trusted-redirect-domain list (exact match or subdomain). The list is
// configured via TRUSTED_REDIRECT_DOMAINS (comma-separated); the server's own
// host is always trusted.
func ValidateRedirectURL(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %s", err.Error())
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("invalid URL scheme: only http and https are allowed")
	}
	domain := strings.ToLower(parsedURL.Hostname())
	if domain == "" {
		return fmt.Errorf("invalid URL: missing host")
	}
	for _, trustedDomain := range TrustedRedirectDomains() {
		if domain == trustedDomain || strings.HasSuffix(domain, "."+trustedDomain) {
			return nil
		}
	}
	return fmt.Errorf("domain %s is not in the trusted domains list", domain)
}

// TrustedRedirectDomains returns the trusted redirect domains: the
// configured TRUSTED_REDIRECT_DOMAINS list plus the server address host.
func TrustedRedirectDomains() []string {
	domains := []string{}
	if raw := GetEnv("TRUSTED_REDIRECT_DOMAINS", ""); raw != "" {
		for _, d := range strings.Split(raw, ",") {
			if t := strings.ToLower(strings.TrimSpace(d)); t != "" {
				domains = append(domains, t)
			}
		}
	}
	if server := GetEnv("SERVER_ADDRESS", ""); server != "" {
		if parsed, err := url.Parse(server); err == nil && parsed.Hostname() != "" {
			domains = append(domains, strings.ToLower(parsed.Hostname()))
		}
	}
	return domains
}
