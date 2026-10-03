package printer

import (
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
)

// T11 (E4): bezpiecznik U4 jest dziś nieosiągalny przez Print (Healthy() ma
// tylko pola z dedykowanymi case'ami), więc emisję sprawdza się wprost: surowa
// linia ~HS to tekst drukarki — od v0.9.0 tylko w logu agenta.
func TestHSFaultErrorKeepsRawLineOutOfMessage(t *testing.T) {
	logs := captureLog(t)
	assertW1(t, hsFaultError(7, HostStatus{Raw: "\x02SEKRET-HS,0,0\x03"}), logs,
		apierr.CodePrinterOffline, 503, "printer fault (~HS)", "SEKRET-HS")
}
