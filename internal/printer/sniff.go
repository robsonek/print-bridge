package printer

import "bytes"

type Format string

const (
	FormatPDF     Format = "PDF"
	FormatZPL     Format = "ZPL"
	FormatUnknown Format = ""
)

// Sniff detects the label format from the raw bytes, ignoring leading whitespace.
// The caller's declared format is only a hint; the sniff is authoritative.
func Sniff(data []byte) Format {
	t := bytes.TrimLeft(data, " \t\r\n")
	if len(t) == 0 {
		return FormatUnknown
	}
	if bytes.HasPrefix(t, []byte("%PDF-")) {
		return FormatPDF
	}
	if bytes.HasPrefix(t, []byte("^XA")) || t[0] == '^' || t[0] == '~' {
		return FormatZPL
	}
	return FormatUnknown
}
