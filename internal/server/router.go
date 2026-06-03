package server

import "net/http"

// Router builds the authenticated HTTP mux.
func Router(h *Handlers, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/print-jobs", h.PrintJobs)
	mux.HandleFunc("GET /api/v1/health", h.HealthHandler)
	mux.HandleFunc("POST /api/v1/admin/update", h.AdminUpdate)
	return TokenAuth(token, mux)
}
