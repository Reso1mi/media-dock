// Package auth contains the HTTP authentication policy shared by every
// MediaDock protocol endpoint.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth protects next with a constant-time Bearer token comparison.
//
// An empty token deliberately leaves the handler unchanged. The application
// only passes an empty token after an explicit development-mode opt-in; this
// low-level package also remains usable behind a trusted reverse proxy.
func BearerAuth(next http.Handler, token string) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ValidBearerToken(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="media-dock"`)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"authentication required","retryable":false,"next_action":"provide_valid_bearer_token"}}
`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ValidBearerToken reports whether header contains exactly one valid Bearer
// token. The scheme comparison is case-insensitive as required by HTTP, while
// the token comparison is constant-time once lengths match.
func ValidBearerToken(header, expected string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	provided := []byte(parts[1])
	wanted := []byte(strings.TrimSpace(expected))
	if len(provided) != len(wanted) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, wanted) == 1
}
