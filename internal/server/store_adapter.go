package server

import "github.com/robsonek/print-bridge/internal/idempotency"

// idemAdapter bridges the concrete idempotency.Store to the server.Store interface.
type idemAdapter struct{ s *idempotency.Store }

func NewStoreAdapter(s *idempotency.Store) Store { return &idemAdapter{s: s} }

func (a *idemAdapter) Get(key string) (StoreRecord, bool, error) {
	r, found, err := a.s.Get(key)
	if err != nil || !found {
		return StoreRecord{}, found, err
	}
	return StoreRecord{ResponseJSON: r.ResponseJSON, CUPSJobID: r.CUPSJobID, Terminal: r.Terminal, Fault: r.Fault}, true, nil
}
func (a *idemAdapter) SavePending(key, job, fault string) error {
	return a.s.SavePending(key, job, fault)
}
func (a *idemAdapter) SaveTerminal(key, body string) error { return a.s.SaveTerminal(key, body) }
