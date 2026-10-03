package printer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
)

// Od v0.9.0 message koperty błędu nie niesie wyjścia narzędzi (pdfinfo,
// pdftoppm, lp) ani tekstu systemów zewnętrznych (IPP, sieć, panel, ~HS):
// etykieta PDF to dane odbiorcy, a pdfinfo cytuje fragmenty dokumentu.
// Granica, która buduje kopertę, loguje pełny błąd (log.Printf) i składa
// message przez publicMessage — nigdy z err.Error().

// publicError niesie tekst dla message obok pełnego błędu. Tekst publiczny
// powstaje WYŁĄCZNIE ze stałych literałów agenta i liczb (kod wyjścia, status
// IPP/HTTP, numer strony). Error() zwraca pełny szczegół bez zmian względem
// v0.8.0 (log agenta, pola health) — nigdy nie trafia do message.
type publicError struct {
	public string
	err    error
}

func (e *publicError) Error() string { return e.err.Error() }
func (e *publicError) Unwrap() error { return e.err }

func withPublic(public string, err error) error { return &publicError{public: public, err: err} }

// ownErrorf: błąd z literałem agenta; argumenty WYŁĄCZNIE liczbowe — tekst
// publiczny i Error() są wtedy identyczne.
func ownErrorf(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return withPublic(msg, errors.New(msg))
}

// toolOutcome opisuje wynik uruchomienia narzędzia bez jego wyjścia (wyjście
// jest tylko w out, który wołający dokleja do pełnego błędu). tool = stała
// nazwa binarki u wołającego.
func toolOutcome(tool string, err error) string {
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return tool + " exited with code " + strconv.Itoa(ee.ExitCode())
	case errors.As(err, &ee):
		return tool + " terminated by signal"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return tool + " not started (request context ended)"
	default:
		return tool + " could not be started"
	}
}

// publicMessage: prefix + ": " + tekst publiczny pierwszego publicError w
// łańcuchu err, a bez niego sam prefix (fail-closed: nieopisany rodzaj błędu
// nie przenosi tekstu do koperty). Nigdy err.Error().
func publicMessage(prefix string, err error) string {
	var pe *publicError
	if errors.As(err, &pe) {
		return prefix + ": " + pe.public
	}
	return prefix
}
