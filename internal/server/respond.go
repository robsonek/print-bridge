// Package server wires HTTP routing, auth, and handlers.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/robsonek/print-bridge/internal/apierr"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, e *apierr.Error) {
	writeJSON(w, e.HTTPStatus, e)
}
