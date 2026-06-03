// Package server wires HTTP routing, auth, and handlers.
package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/robsonek/print-bridge/internal/apierr"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// #26: headers + status are already on the wire, so this is observability
		// only (e.g. the client disconnected mid-write during the ~30s poll, or a
		// broken pipe). Silently swallowing it hid health/error-body truncation;
		// log it so the failure is at least diagnosable.
		log.Printf("writeJSON: response encode/write failed (status=%d): %v", status, err)
	}
}

func writeError(w http.ResponseWriter, e *apierr.Error) {
	writeJSON(w, e.HTTPStatus, e)
}
