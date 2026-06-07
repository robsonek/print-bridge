package apierr

import (
	"encoding/json"
	"testing"
)

func TestRetryableClassification(t *testing.T) {
	retry := []Code{CodeCUPSUnavailable, CodePrinterOffline, CodeOutOfPaper, CodeQueuePaused, CodePrintTimeout, CodeBridgeRestarting}
	for _, c := range retry {
		if !c.Retryable() {
			t.Errorf("%s should be retryable", c)
		}
	}
	noRetry := []Code{CodeInvalidPDF, CodeInvalidZPL, CodeUnsupportedFormat, CodeInvalidRequest, CodeMissingToken, CodeForbidden, CodePrintUnconfirmed}
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
