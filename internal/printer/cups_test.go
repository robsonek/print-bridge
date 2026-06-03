package printer

import (
	"testing"

	"github.com/OpenPrinting/goipp"
)

func TestParseJobID(t *testing.T) {
	id, err := parseJobID("request id is XP423B-42 (1 file(s))\n")
	if err != nil {
		t.Fatalf("parseJobID: %v", err)
	}
	if id != 42 {
		t.Errorf("job id = %d, want 42", id)
	}
}

func TestParseJobIDError(t *testing.T) {
	if _, err := parseJobID("lp: Error - no such destination\n"); err == nil {
		t.Fatal("expected error when lp output has no request id")
	}
}

func TestJobStateConstants(t *testing.T) {
	if JobCompleted != 9 || JobProcessing != 5 || JobAborted != 8 || JobCanceled != 7 {
		t.Error("IPP job-state constants must match RFC 8011")
	}
}

// #6 regression: a successful IPP status (success set) must pass checkIPPStatus,
// while any error status must be surfaced as an error so JobState does not let a
// CUPS failure masquerade as "job-state not found" and silently halt the print.
func TestCheckIPPStatus(t *testing.T) {
	successes := []goipp.Status{
		goipp.StatusOk,
		goipp.StatusOkIgnoredOrSubstituted,
		goipp.StatusOkConflicting,
	}
	for _, st := range successes {
		msg := &goipp.Message{Code: goipp.Code(st)}
		if err := checkIPPStatus(msg); err != nil {
			t.Errorf("status 0x%04x must be OK, got error %v", uint16(st), err)
		}
	}

	failures := []goipp.Status{
		goipp.StatusErrorNotFound,
		goipp.StatusErrorForbidden,
		goipp.StatusErrorInternal,
		goipp.StatusErrorNotAcceptingJobs,
	}
	for _, st := range failures {
		msg := &goipp.Message{Code: goipp.Code(st)}
		if err := checkIPPStatus(msg); err == nil {
			t.Errorf("status 0x%04x must yield an error, got nil", uint16(st))
		}
	}
}
