package apierr

import (
	"encoding/json"
	"testing"
)

func TestRetryableClassification(t *testing.T) {
	// PRINTER_BUSY: Retryable() w Go, choć klient ponawia go RĘCZNIE (409 tylko
	// z resetu) — docs/error-contract.md §3.
	retry := []Code{CodeCUPSUnavailable, CodePrinterOffline, CodeOutOfPaper, CodeQueuePaused, CodePrintTimeout, CodeBridgeRestarting, CodePrinterBusy}
	for _, c := range retry {
		if !c.Retryable() {
			t.Errorf("%s should be retryable", c)
		}
	}
	// UPDATE_FAILED / UPDATE_IN_PROGRESS (od v0.8.0): mutacje admin — operator
	// ponawia ręcznie, nigdy automat (docs/error-contract.md §3).
	noRetry := []Code{CodeInvalidPDF, CodeInvalidZPL, CodeUnsupportedFormat, CodeInvalidRequest, CodeMissingToken, CodeForbidden, CodePrintUnconfirmed, CodeUpdateFailed, CodeUpdateInProgress}
	for _, c := range noRetry {
		if c.Retryable() {
			t.Errorf("%s should NOT be retryable", c)
		}
	}
}

func TestErrorJSONShape(t *testing.T) {
	e := New(CodeOutOfPaper, "brak papieru", 503)
	raw, _ := json.Marshal(e)
	var got map[string]any
	json.Unmarshal(raw, &got)
	if got["code"] != "PRINTER_OUT_OF_PAPER" {
		t.Errorf("code = %v", got["code"])
	}
	if _, hasStatus := got["http_status"]; hasStatus {
		t.Error("http_status must NOT serialize (json:\"-\")")
	}
	if e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d, want 503", e.HTTPStatus)
	}
}
