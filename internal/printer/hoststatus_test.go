package printer

import "testing"

func TestParseHostStatusPaperOut(t *testing.T) {
	// Zebra ~HS line 1: aaa,b,c,... where field[1]=paper out (1), field[2]=pause (1).
	hs, ok := ParseHostStatus("030,1,0,1234,000,0,0,0,000,0,0,0")
	if !ok {
		t.Fatal("parse should succeed for a well-formed line")
	}
	if !hs.PaperOut {
		t.Error("PaperOut should be true (field[1]=1)")
	}
	if hs.Paused {
		t.Error("Paused should be false (field[2]=0)")
	}
}

func TestParseHostStatusHealthy(t *testing.T) {
	hs, ok := ParseHostStatus("030,0,0,1234,000,0,0,0,000,0,0,0")
	if !ok || hs.PaperOut || hs.Paused {
		t.Errorf("healthy parse wrong: %+v ok=%v", hs, ok)
	}
	if !hs.Healthy() {
		t.Error("Healthy() should be true when no fault flags set")
	}
}

func TestParseHostStatusUnparseable(t *testing.T) {
	if _, ok := ParseHostStatus("garbage"); ok {
		t.Error("unparseable input must report ok=false (triggers graceful degrade)")
	}
}
