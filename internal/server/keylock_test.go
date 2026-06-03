package server

import (
	"sync"
	"testing"
)

// Same key => mutual exclusion: no two critical sections overlap.
func TestKeyLockSerializesSameKey(t *testing.T) {
	kl := NewKeyLock()
	var (
		mu        sync.Mutex
		inside    int
		maxInside int
	)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := kl.Lock("same")
			defer unlock()
			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()
			// brief critical section
			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Errorf("same key must serialize: max concurrent inside = %d, want 1", maxInside)
	}
}

// Different keys do not block each other and entries are cleaned up.
func TestKeyLockDistinctKeysAndCleanup(t *testing.T) {
	kl := NewKeyLock()
	u1 := kl.Lock("a")
	u2 := kl.Lock("b") // must not deadlock on a different key
	u1()
	u2()

	kl.mu.Lock()
	n := len(kl.locks)
	kl.mu.Unlock()
	if n != 0 {
		t.Errorf("lock map must be empty after release, got %d entries", n)
	}
}
