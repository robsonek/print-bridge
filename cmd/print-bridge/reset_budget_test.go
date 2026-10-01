package main

import (
	"testing"
	"time"
)

// U3 (od v0.8.0): budżet resetu = min(WriteTimeout − 10 s, 100 s). Margines
// 10 s pokrywa dokończenie sondy ~HS (ctx tylko przy dial, ~5,3 s), a sufit
// 100 s trzyma reset poniżej domyślnego client_timeout klienta (120 s).
func TestResetBudget(t *testing.T) {
	cases := []struct {
		confirmSec int
		want       time.Duration
	}{
		{30, 80 * time.Second}, // domyślnie: WriteTimeout 90 s
		{5, 55 * time.Second},
		{50, 100 * time.Second}, // sufit
		{300, 100 * time.Second},
	}
	for _, c := range cases {
		got := resetBudget(c.confirmSec)
		if got != c.want {
			t.Errorf("resetBudget(%d) = %v, want %v", c.confirmSec, got, c.want)
		}
		if wt := writeTimeout(c.confirmSec); got > wt-10*time.Second {
			t.Errorf("resetBudget(%d) = %v, a WriteTimeout %v — margines < 10 s", c.confirmSec, got, wt)
		}
	}
}
