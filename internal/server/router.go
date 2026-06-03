package server

import "net/http"

// Router builds the authenticated HTTP mux.
//
// #23 (accepted risk): there is intentionally NO rate-limiting/throttling
// middleware here. This is a single-tenant, internal warehouse-VM agent reachable
// ONLY from the single the marketplace orchestrator egress IP (ufw allow from <egress_cidr>,
// see deploy/install-debian.sh) and gated by a 64+ char constant-time X-Print-Token
// (TokenAuth). A per-IP token bucket adds nothing on top of that threat model:
// an actor who has already defeated the IP allow-list AND holds the token is past
// any limiter, and guessing a high-entropy token over the network is infeasible
// regardless. The only realistic footgun (a buggy trusted caller spamming
// admin/update) is better addressed by an in-flight guard on that handler than by
// a global limiter. Deliberately omitted, not overlooked.
func Router(h *Handlers, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/print-jobs", h.PrintJobs)
	mux.HandleFunc("GET /api/v1/health", h.HealthHandler)
	mux.HandleFunc("POST /api/v1/admin/update", h.AdminUpdate)
	return TokenAuth(token, mux)
}
