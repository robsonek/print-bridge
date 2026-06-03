package printer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// ippStatusServer stands up a fake CUPS that replies to every IPP request with a
// response whose operation status == st and an empty group set (no job-state).
func ippStatusServer(t *testing.T, st goipp.Status) *CUPSClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := goipp.NewResponse(goipp.DefaultVersion, st, 1)
		resp.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
		resp.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en")))
		body, err := resp.EncodeBytes()
		if err != nil {
			t.Errorf("encode IPP response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &CUPSClient{
		queue:  "test",
		ippURL: srv.URL,
		httpc:  &http.Client{Timeout: 2 * time.Second},
	}
}

// #6 regression: an IPP client-error-not-found / client-error-gone (job purged
// from CUPS history) must surface as the ErrJobGone sentinel so pollAndVerify can
// route the resume-by-key recovery path to a best-effort ~HS verify instead of a
// hard CUPS_UNAVAILABLE for a label that physically printed.
func TestJobStateGoneReturnsSentinel(t *testing.T) {
	for _, st := range []goipp.Status{goipp.StatusErrorNotFound, goipp.StatusErrorGone} {
		c := ippStatusServer(t, st)
		_, err := c.JobState(context.Background(), 7)
		if !errors.Is(err, ErrJobGone) {
			t.Errorf("status 0x%04x must map to ErrJobGone, got %v", uint16(st), err)
		}
	}
}

// #6 regression: a non-eviction IPP error (e.g. forbidden) must NOT be swallowed
// as ErrJobGone — it stays a descriptive error so pollAndVerify maps it to the
// hard CUPS_UNAVAILABLE/503 (and the precise IPP code is preserved for diag).
func TestJobStateForbiddenIsNotGone(t *testing.T) {
	c := ippStatusServer(t, goipp.StatusErrorForbidden)
	_, err := c.JobState(context.Background(), 7)
	if err == nil {
		t.Fatal("forbidden must yield an error")
	}
	if errors.Is(err, ErrJobGone) {
		t.Errorf("forbidden must NOT be ErrJobGone, got %v", err)
	}
	if !strings.Contains(err.Error(), "0x0401") {
		t.Errorf("error must carry the IPP status code, got %v", err)
	}
}
