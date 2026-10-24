package router_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/router"
)

func TestRouter_LongestPrefixMatching(t *testing.T) {
	routes := []config.RouteConfig{
		{
			ID:           "api-root",
			PathPrefix:   "/api",
			Methods:      []string{http.MethodGet},
			LoadBalancer: "round_robin",
			Timeout:      1 * time.Second,
		},
		{
			ID:           "api-users-detailed",
			PathPrefix:   "/api/users/profile",
			Methods:      []string{http.MethodGet, http.MethodPost},
			LoadBalancer: "round_robin",
			Timeout:      1 * time.Second,
		},
		{
			ID:           "api-users",
			PathPrefix:   "/api/users",
			Methods:      []string{http.MethodGet},
			LoadBalancer: "round_robin",
			Timeout:      1 * time.Second,
		},
	}

	r := router.NewRouter(routes)

	tests := []struct {
		name          string
		path          string
		method        string
		expectedRoute string
		expectMatched bool
		expectMethod  bool
	}{
		{
			name:          "exact match longest prefix",
			path:          "/api/users/profile",
			method:        http.MethodGet,
			expectedRoute: "api-users-detailed",
			expectMatched: true,
			expectMethod:  true,
		},
		{
			name:          "sub-path of longest prefix",
			path:          "/api/users/profile/photo",
			method:        http.MethodPost,
			expectedRoute: "api-users-detailed",
			expectMatched: true,
			expectMethod:  true,
		},
		{
			name:          "medium prefix match",
			path:          "/api/users/123",
			method:        http.MethodGet,
			expectedRoute: "api-users",
			expectMatched: true,
			expectMethod:  true,
		},
		{
			name:          "short root prefix match",
			path:          "/api/orders",
			method:        http.MethodGet,
			expectedRoute: "api-root",
			expectMatched: true,
			expectMethod:  true,
		},
		{
			name:          "method not allowed on matched route",
			path:          "/api/users/123",
			method:        http.MethodDelete,
			expectedRoute: "api-users",
			expectMatched: true,
			expectMethod:  false,
		},
		{
			name:          "no route match",
			path:          "/other/path",
			method:        http.MethodGet,
			expectedRoute: "",
			expectMatched: false,
			expectMethod:  false,
		},
		{
			name:          "prefix boundary isolation (must not match /api_something)",
			path:          "/api_something",
			method:        http.MethodGet,
			expectedRoute: "",
			expectMatched: false,
			expectMethod:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := r.Match(tc.path, tc.method)
			if res.PathMatched != tc.expectMatched {
				t.Fatalf("expected PathMatched %v, got %v", tc.expectMatched, res.PathMatched)
			}
			if tc.expectMatched {
				if res.Route.ID != tc.expectedRoute {
					t.Errorf("expected route ID %s, got %s", tc.expectedRoute, res.Route.ID)
				}
				if res.MethodAllowed != tc.expectMethod {
					t.Errorf("expected MethodAllowed %v, got %v", tc.expectMethod, res.MethodAllowed)
				}
				if !res.MethodAllowed && len(res.AllowedMethods) == 0 {
					t.Errorf("expected non-empty AllowedMethods on method mismatch")
				}
			}
		})
	}
}

func TestRouter_ReservedPathsCannotBeShadowed(t *testing.T) {
	routes := []config.RouteConfig{
		{
			ID:           "wildcard-catch",
			PathPrefix:   "/",
			Methods:      []string{http.MethodGet, http.MethodPost},
			LoadBalancer: "round_robin",
			Timeout:      1 * time.Second,
		},
	}

	r := router.NewRouter(routes)

	reserved := []string{"/healthz", "/readyz", "/metrics", "/-/status"}
	for _, p := range reserved {
		res := r.Match(p, http.MethodGet)
		if !res.IsReserved {
			t.Errorf("expected path %s to be recognized as reserved system path", p)
		}
	}
}
