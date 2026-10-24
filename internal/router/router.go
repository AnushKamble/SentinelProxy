package router

import (
	"path"
	"sort"
	"strings"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

var reservedPaths = map[string]bool{
	"/healthz":  true,
	"/readyz":   true,
	"/metrics":  true,
	"/-/status": true,
}

// MatchResult details the result of matching an incoming HTTP request.
type MatchResult struct {
	Route          *config.RouteConfig
	PathMatched    bool
	MethodAllowed  bool
	AllowedMethods []string
	IsReserved     bool
}

// Router provides deterministic longest-prefix path matching and method policy enforcement.
type Router struct {
	sortedRoutes []config.RouteConfig
}

// NewRouter constructs a Router from configured routes.
func NewRouter(routes []config.RouteConfig) *Router {
	sorted := make([]config.RouteConfig, len(routes))
	copy(sorted, routes)

	// Sort routes descending by PathPrefix length for deterministic longest-prefix match.
	// For equal lengths, sort alphabetically by ID.
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i].PathPrefix) != len(sorted[j].PathPrefix) {
			return len(sorted[i].PathPrefix) > len(sorted[j].PathPrefix)
		}
		return sorted[i].ID < sorted[j].ID
	})

	return &Router{
		sortedRoutes: sorted,
	}
}

// Match evaluates a request path and method deterministically.
func (r *Router) Match(reqPath string, method string) MatchResult {
	// Clean path to prevent path traversal tricks
	cleanPath := path.Clean(reqPath)
	if !strings.HasPrefix(cleanPath, "/") {
		cleanPath = "/" + cleanPath
	}

	// Reserved paths have strict precedence and cannot be shadowed
	if reservedPaths[cleanPath] {
		return MatchResult{
			IsReserved: true,
		}
	}

	for i := range r.sortedRoutes {
		route := &r.sortedRoutes[i]
		prefix := route.PathPrefix

		// Check if cleanPath matches prefix either exactly or as a directory prefix
		// e.g. prefix "/api/users" matches "/api/users" or "/api/users/123", but not "/api/users_other"
		if cleanPath == prefix || strings.HasPrefix(cleanPath, strings.TrimSuffix(prefix, "/")+"/") {
			methodAllowed := false
			for _, m := range route.Methods {
				if m == method {
					methodAllowed = true
					break
				}
			}

			return MatchResult{
				Route:          route,
				PathMatched:    true,
				MethodAllowed:  methodAllowed,
				AllowedMethods: route.Methods,
				IsReserved:     false,
			}
		}
	}

	return MatchResult{
		PathMatched:   false,
		MethodAllowed: false,
		IsReserved:    false,
	}
}
