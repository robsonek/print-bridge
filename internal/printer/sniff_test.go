package printer

import "testing"

func TestSniff(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want Format
	}{
		{"pdf magic", []byte("%PDF-1.7\n..."), FormatPDF},
		{"pdf with leading ws", []byte("\n  %PDF-1.4"), FormatPDF},
		{"zpl xa", []byte("^XA^FO50,50^A0N^FDhi^FS^XZ"), FormatZPL},
		{"zpl tilde cmd", []byte("~HS"), FormatZPL},
		{"garbage", []byte("hello world"), FormatUnknown},
		{"empty", []byte(""), FormatUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sniff(tc.in); got != tc.want {
				t.Errorf("Sniff = %q, want %q", got, tc.want)
			}
		})
	}
}
