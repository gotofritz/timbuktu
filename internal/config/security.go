package config

import (
	"fmt"
	"net"
	"net/url"
)

// ValidateKeyedBaseURL rejects a base URL that would leak an attached API key.
// A key is safe to send over HTTPS (any host) or to a loopback address (any
// scheme — a local proxy). Any other scheme to a non-loopback host would send
// the credential across the network unencrypted, so it is refused. Callers use
// this in cloud-provider factories, where an API key is always attached.
func ValidateKeyedBaseURL(baseURL string) error {
	if baseURL == "" {
		return fmt.Errorf("config: empty base_url with an API key set")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("config: invalid base_url %q", baseURL)
	}
	if u.Scheme == "https" {
		return nil
	}
	if isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf(
		"config: refusing to send API key to non-HTTPS base_url %q — the credential "+
			"would cross the network unencrypted; use https:// or a loopback address",
		baseURL)
}

// isLoopbackHost reports whether host is localhost or a loopback IP.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
