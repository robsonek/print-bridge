// Package apierr defines the wire error contract shared with the Laravel side.
package apierr

type Code string

const (
	CodeCUPSUnavailable   Code = "CUPS_UNAVAILABLE"
	CodePrinterOffline    Code = "PRINTER_OFFLINE"
	CodeOutOfPaper        Code = "PRINTER_OUT_OF_PAPER"
	CodeQueuePaused       Code = "QUEUE_PAUSED"
	CodePrintTimeout      Code = "PRINT_TIMEOUT"
	CodeBridgeRestarting  Code = "BRIDGE_RESTARTING"
	CodeInvalidPDF        Code = "INVALID_PDF"
	CodeInvalidZPL        Code = "INVALID_ZPL"
	CodeUnsupportedFormat Code = "UNSUPPORTED_FORMAT"
	CodeInvalidRequest    Code = "INVALID_REQUEST"
	CodeMissingToken      Code = "MISSING_TOKEN"
	CodeForbidden         Code = "FORBIDDEN"
	CodePrinterBusy       Code = "PRINTER_BUSY"
	// CodePrintUnconfirmed: job po faulcie sprzętowym — fizyczny wynik
	// NIEOBSERWOWALNY (format mógł zostać odrzucony LUB wydrukowany przy
	// recovery; dowód: hardware-spike-findings.md, test paper-out 2026-06-07).
	// Wymaga decyzji człowieka (potwierdź/dodrukuj NOWYM kluczem) — celowo
	// NIE-retryable, żeby automat nie pętlił i nie wymuszał fałszywego printed.
	CodePrintUnconfirmed Code = "PRINT_UNCONFIRMED"
)

var retryable = map[Code]bool{
	CodeCUPSUnavailable:  true,
	CodePrinterOffline:   true,
	CodeOutOfPaper:       true,
	CodeQueuePaused:      true,
	CodePrintTimeout:     true,
	CodeBridgeRestarting: true,
	CodePrinterBusy:      true, // druk w toku — spróbuj po jego zakończeniu
}

func (c Code) Retryable() bool { return retryable[c] }

// Error is both a Go error and the JSON body returned to clients.
type Error struct {
	Code       Code           `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	HTTPStatus int            `json:"-"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func New(code Code, msg string, httpStatus int) *Error {
	return &Error{Code: code, Message: msg, HTTPStatus: httpStatus}
}

func (e *Error) WithDetail(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}
