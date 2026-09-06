package relay

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	defaultRelayIdleConnTimeout     = 90 * time.Second
	defaultRelayMaxIdleConns        = 500
	defaultRelayMaxIdleConnsPerHost = 100

	minRelayIdleConnTimeoutSeconds = 1
	maxRelayIdleConnTimeoutSeconds = 24 * 60 * 60
	minRelayIdleConnections        = 1
	maxRelayIdleConnections        = 10_000
)

type relayHTTPTransportConfig struct {
	idleConnTimeout     time.Duration
	maxIdleConns        int
	maxIdleConnsPerHost int
	insecureSkipVerify  bool
}

func defaultRelayHTTPTransportConfig() relayHTTPTransportConfig {
	return relayHTTPTransportConfig{
		idleConnTimeout:     defaultRelayIdleConnTimeout,
		maxIdleConns:        defaultRelayMaxIdleConns,
		maxIdleConnsPerHost: defaultRelayMaxIdleConnsPerHost,
	}
}

type relayEnvironmentLookup func(string) (string, bool)

func loadRelayHTTPTransportConfig(lookup relayEnvironmentLookup) (relayHTTPTransportConfig, error) {
	config := defaultRelayHTTPTransportConfig()

	idleSeconds, err := boundedRelayEnvironmentInt(
		lookup,
		"RELAY_IDLE_CONN_TIMEOUT",
		int(config.idleConnTimeout/time.Second),
		minRelayIdleConnTimeoutSeconds,
		maxRelayIdleConnTimeoutSeconds,
	)
	if err != nil {
		return relayHTTPTransportConfig{}, err
	}
	config.idleConnTimeout = time.Duration(idleSeconds) * time.Second

	config.maxIdleConns, err = boundedRelayEnvironmentInt(
		lookup,
		"RELAY_MAX_IDLE_CONNS",
		config.maxIdleConns,
		minRelayIdleConnections,
		maxRelayIdleConnections,
	)
	if err != nil {
		return relayHTTPTransportConfig{}, err
	}
	config.maxIdleConnsPerHost, err = boundedRelayEnvironmentInt(
		lookup,
		"RELAY_MAX_IDLE_CONNS_PER_HOST",
		config.maxIdleConnsPerHost,
		minRelayIdleConnections,
		maxRelayIdleConnections,
	)
	if err != nil {
		return relayHTTPTransportConfig{}, err
	}
	config.insecureSkipVerify, err = relayEnvironmentBool(lookup, "TLS_INSECURE_SKIP_VERIFY", false)
	if err != nil {
		return relayHTTPTransportConfig{}, err
	}
	return config, nil
}

func boundedRelayEnvironmentInt(
	lookup relayEnvironmentLookup,
	key string,
	defaultValue int,
	minimum int,
	maximum int,
) (int, error) {
	raw, present := lookup(key)
	if !present {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(value) != raw || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be a base-10 integer between %d and %d", key, minimum, maximum)
	}
	return value, nil
}

func relayEnvironmentBool(lookup relayEnvironmentLookup, key string, defaultValue bool) (bool, error) {
	raw, present := lookup(key)
	if !present {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func newRelayHTTPClient(config relayHTTPTransportConfig) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: config.insecureSkipVerify},
			MaxIdleConns:          config.maxIdleConns,
			MaxIdleConnsPerHost:   config.maxIdleConnsPerHost,
			IdleConnTimeout:       config.idleConnTimeout,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     true,
			DialContext:           common.SafeDialContext,
		},
		// Relay requests carry provider credentials. A redirect is never part
		// of the provider contract and may replay credentials to a new origin.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// InitHTTPClient validates the complete relay transport configuration before
// publishing it. It is a startup initializer and must run after .env loading
// and before the router begins serving requests.
func InitHTTPClient() error {
	config, err := loadRelayHTTPTransportConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	candidate := newRelayHTTPClient(config)
	previous := relayHTTPClient
	relayHTTPClient = candidate
	if previous != nil {
		previous.CloseIdleConnections()
	}
	return nil
}
