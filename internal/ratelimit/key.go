package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// KeyExtractor determines the rate-limiting client identity from HTTP requests.
type KeyExtractor struct {
	trustedCIDRs []*net.IPNet
}

// NewKeyExtractor creates a KeyExtractor with configured trusted proxy networks.
func NewKeyExtractor(trustedCIDRStrings []string) (*KeyExtractor, error) {
	var nets []*net.IPNet
	for _, raw := range trustedCIDRStrings {
		_, ipNet, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, err
		}
		nets = append(nets, ipNet)
	}
	return &KeyExtractor{trustedCIDRs: nets}, nil
}

// ExtractKey determines the rate-limit bucket key in order of precedence:
// 1. Authenticated JWT subject (if present)
// 2. Client IP from X-Forwarded-For (ONLY if direct peer IP is in trusted CIDRs)
// 3. Direct peer IP from RemoteAddr
func (ke *KeyExtractor) ExtractKey(r *http.Request, authenticatedSub string) string {
	if authenticatedSub != "" {
		return "sub:" + authenticatedSub
	}

	peerIPStr := extractIP(r.RemoteAddr)
	peerIP := net.ParseIP(peerIPStr)

	// Check if peer is in trusted proxy range
	if peerIP != nil && ke.isTrustedProxy(peerIP) {
		xff := r.Header.Get("X-Forwarded-For")
		if xff != "" {
			parts := strings.Split(xff, ",")
			clientIP := strings.TrimSpace(parts[0])
			if parsed := net.ParseIP(clientIP); parsed != nil {
				return "ip:" + parsed.String()
			}
		}
	}

	if peerIPStr != "" {
		return "ip:" + peerIPStr
	}

	return "ip:unknown"
}

func (ke *KeyExtractor) isTrustedProxy(ip net.IP) bool {
	for _, ipNet := range ke.trustedCIDRs {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}
