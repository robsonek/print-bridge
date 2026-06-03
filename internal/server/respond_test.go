package server

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"strings"
	"testing"
)

// failingWriter is a ResponseWriter whose Write always fails, simulating a
// client disconnect mid-response (broken pipe).
type failingWriter struct {
	header http.Header
	status int
}

func newFailingWriter() *failingWriter { return &failingWriter{header: http.Header{}} }

func (f *failingWriter) Header() http.Header { return f.header }
func (f *failingWriter) WriteHeader(s int)   { f.status = s }
func (f *failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

// Regression (#26): writeJSON must LOG when the encoder/writer fails instead of
// silently swallowing the error.
func TestWriteJSONLogsEncodeError(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	w := newFailingWriter()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	out := buf.String()
	if !strings.Contains(out, "writeJSON") || !strings.Contains(out, "broken pipe") {
		t.Errorf("expected encode/write failure to be logged, got: %q", out)
	}
}

// A normal write must NOT log anything.
func TestWriteJSONNoLogOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	w := newRecordingWriter()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	if buf.Len() != 0 {
		t.Errorf("successful write must not log, got: %q", buf.String())
	}
}

// recordingWriter is a minimal in-memory ResponseWriter that succeeds.
type recordingWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newRecordingWriter() *recordingWriter { return &recordingWriter{header: http.Header{}} }

func (r *recordingWriter) Header() http.Header         { return r.header }
func (r *recordingWriter) WriteHeader(s int)           { r.status = s }
func (r *recordingWriter) Write(b []byte) (int, error) { return r.body.Write(b) }
