package server

import (
	"crypto/subtle"
	"net/http"

	"github.com/robsonek/print-bridge/internal/apierr"
)

// TokenAuth enforces X-Print-Token (constant-time), fail-closed, except /health.
func TokenAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-Print-Token")
		if provided == "" {
			writeError(w, apierr.New(apierr.CodeMissingToken, "X-Print-Token header required", http.StatusUnauthorized))
			return
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			writeError(w, apierr.New(apierr.CodeForbidden, "invalid token", http.StatusForbidden))
			return
		}
		next.ServeHTTP(w, r)
	})
}
