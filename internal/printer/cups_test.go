package printer

import "testing"

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
