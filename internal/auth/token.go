// Package auth provides bearer-token HTTP middleware.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Bearer returns middleware that requires Authorization: Bearer <expected>.
// Comparison is constant-time. An empty expected token panics at construction
// time — a missing token is a startup error, not a runtime 401.
func Bearer(expected string) func(http.Handler) http.Handler {
	if expected == "" {
		panic("auth.Bearer: empty token")
	}
	expectedBytes := []byte(expected)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="funnel-manager"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			got := []byte(strings.TrimPrefix(h, prefix))
			if subtle.ConstantTimeCompare(got, expectedBytes) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
