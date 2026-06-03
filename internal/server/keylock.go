package server

import "sync"

// KeyLock provides per-key mutual exclusion so concurrent requests carrying the
// same Idempotency-Key are serialized end-to-end (Get -> resume/print -> persist).
// This closes the check-then-act race (#3,#5): without it, two parallel same-key
// requests both see Get=not-found during the ~30s print window and both Submit,
// producing a duplicate physical label.
//
// The lock is process-local. Cross-process / cross-restart duplication is handled
// by the persisted idempotency store (resume-by-key); the KeyLock only guards the
// in-process concurrency window.
type KeyLock struct {
	mu    sync.Mutex
	locks map[string]*keyEntry
}

type keyEntry struct {
	mu       sync.Mutex
	refCount int
}

// NewKeyLock returns a ready-to-use KeyLock.
func NewKeyLock() *KeyLock {
	return &KeyLock{locks: make(map[string]*keyEntry)}
}

// Lock acquires the per-key mutex for key and returns an unlock function. The
// returned function MUST be called exactly once (typically via defer). Entries
// are reference-counted and removed from the map once no caller holds them, so
// the map does not grow unbounded across distinct keys.
func (k *KeyLock) Lock(key string) func() {
	k.mu.Lock()
	e, ok := k.locks[key]
	if !ok {
		e = &keyEntry{}
		k.locks[key] = e
	}
	e.refCount++
	k.mu.Unlock()

	e.mu.Lock()

	return func() {
		e.mu.Unlock()

		k.mu.Lock()
		e.refCount--
		if e.refCount == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
