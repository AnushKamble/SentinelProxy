package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// TransportConfig defines pooling and timeout options for upstream HTTP connections.
type TransportConfig struct {
	DialTimeout           time.Duration
	KeepAlive             time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
}

// DefaultTransportConfig returns production-safe connection pooling defaults.
func DefaultTransportConfig() TransportConfig {
	return TransportConfig{
		DialTimeout:           5 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		MaxConnsPerHost:       0, // unlimited total connections per host, bounded by caller/ratelimit
	}
}

// NewPooledTransport creates an http.Transport configured with connection pooling and timeouts.
func NewPooledTransport(cfg TransportConfig) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: cfg.KeepAlive,
	}

	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
}
