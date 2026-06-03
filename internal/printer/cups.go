package printer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/OpenPrinting/goipp"
)

// ErrJobGone means CUPS no longer has the job in its history (purged/evicted via
// PreserveJobHistory/MaxJobs). For a job we already submitted, "gone" most likely
// means it completed, so pollAndVerify routes this to a best-effort ~HS verify
// instead of a hard CUPS_UNAVAILABLE failure. It is returned as a bare sentinel
// (no wrapping) so callers can match it with errors.Is.
var ErrJobGone = errors.New("ipp: job not found in CUPS history")

// IPP job-state values (RFC 8011 §5.3.7).
const (
	JobPending           = 3
	JobPendingHeld       = 4
	JobProcessing        = 5
	JobProcessingStopped = 6
	JobCanceled          = 7
	JobAborted           = 8
	JobCompleted         = 9
)

// CUPSClient submits raw jobs via `lp -o raw` and polls job-state via IPP.
type CUPSClient struct {
	queue  string
	ippURL string // http://localhost:631/printers/<queue>
	httpc  *http.Client
}

func NewCUPSClient(queue string) *CUPSClient {
	return &CUPSClient{
		queue:  queue,
		ippURL: fmt.Sprintf("http://localhost:631/printers/%s", queue),
		httpc:  &http.Client{Timeout: 10 * time.Second},
	}
}

var jobIDRe = regexp.MustCompile(`request id is \S+-(\d+)`)

func parseJobID(lpOutput string) (int, error) {
	m := jobIDRe.FindStringSubmatch(lpOutput)
	if m == nil {
		return 0, fmt.Errorf("no job id in lp output: %q", lpOutput)
	}
	return strconv.Atoi(m[1])
}

// buildSubmitPayload realizes `copies` by REPLICATING the whole ZPL stream, not
// via `lp -n`. #14: a raw queue over a socket:9100 backend does NOT honor the IPP
// `copies` attribute for stdin input — CUPS' socket backend forces copies=1
// (backend/socket.c: print_fd==0 => copies=1; it cannot lseek() a pipe to resend),
// and raw ZPL carries no ^PQ quantity command, so `lp -o raw -n N` prints exactly
// ONE label. Each ^XA..^XZ is a self-contained label, so concatenating the stream
// N times feeds N labels deterministically and language-agnostically (the socket
// backend just passes the bytes through). copies<=1 returns the data unchanged.
func buildSubmitPayload(data []byte, copies int) []byte {
	if copies <= 1 {
		return data
	}
	return bytes.Repeat(data, copies)
}

// Submit pipes the data (ZPL) to `lp -o raw` and returns the CUPS job id. copies>1
// is realized by replicating the ZPL stream in the payload (see buildSubmitPayload),
// NOT by `lp -n` which the socket backend ignores for raw stdin jobs (#14).
func (c *CUPSClient) Submit(ctx context.Context, data []byte, copies int) (int, error) {
	payload := buildSubmitPayload(data, copies)
	cmd := exec.CommandContext(ctx, "lp", "-d", c.queue, "-o", "raw")
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("lp failed: %v: %s", err, out)
	}
	return parseJobID(string(out))
}

// JobState queries IPP Get-Job-Attributes for the job's current state.
func (c *CUPSClient) JobState(ctx context.Context, jobID int) (int, error) {
	req := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetJobAttributes, 1)
	req.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	req.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en")))
	req.Operation.Add(goipp.MakeAttribute("job-uri", goipp.TagURI, goipp.String(fmt.Sprintf("ipp://localhost/jobs/%d", jobID))))
	req.Operation.Add(goipp.MakeAttribute("requested-attributes", goipp.TagKeyword, goipp.String("job-state")))

	resp, err := c.doIPP(ctx, req)
	if err != nil {
		// #6: doIPP returns the decoded message even on an IPP error status, so we
		// can distinguish "job evicted from CUPS history" (not-found / gone) from a
		// genuine failure. The former is the expected outcome on the resume-by-key
		// recovery path after CUPS purges completed-job history -> map to the
		// ErrJobGone sentinel so pollAndVerify can fall back to ~HS verify instead
		// of treating a printed job as a hard CUPS_UNAVAILABLE error.
		if resp != nil {
			switch goipp.Status(resp.Code) {
			case goipp.StatusErrorNotFound, goipp.StatusErrorGone:
				return 0, ErrJobGone
			}
		}
		return 0, err
	}
	for _, g := range resp.Groups {
		for _, a := range g.Attrs {
			if a.Name == "job-state" && len(a.Values) > 0 {
				if iv, ok := a.Values[0].V.(goipp.Integer); ok {
					return int(iv), nil
				}
			}
		}
	}
	return 0, fmt.Errorf("job-state not found in IPP response")
}

// PrinterReasons queries IPP Get-Printer-Attributes for printer-state-reasons.
func (c *CUPSClient) PrinterReasons(ctx context.Context) ([]string, error) {
	req := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetPrinterAttributes, 1)
	req.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	req.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en")))
	req.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(c.ippURL)))
	req.Operation.Add(goipp.MakeAttribute("requested-attributes", goipp.TagKeyword, goipp.String("printer-state-reasons")))

	resp, err := c.doIPP(ctx, req)
	if err != nil {
		return nil, err
	}
	var reasons []string
	for _, g := range resp.Groups {
		for _, a := range g.Attrs {
			if a.Name == "printer-state-reasons" {
				for _, v := range a.Values {
					reasons = append(reasons, v.V.String())
				}
			}
		}
	}
	return reasons, nil
}

func (c *CUPSClient) doIPP(ctx context.Context, req *goipp.Message) (*goipp.Message, error) {
	payload, err := req.EncodeBytes()
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ippURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/ipp")
	httpResp, err := c.httpc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	var resp goipp.Message
	if err := resp.Decode(httpResp.Body); err != nil {
		return nil, err
	}
	// #6: the IPP response status lives in resp.Code (goipp: "Operation for
	// request, status for response"). Decode populates it even on error
	// responses, so check it here. Without this, an IPP error (e.g. job purged ->
	// client-error-not-found 0x0406, or forbidden 0x0401) returns a message with
	// no job group; JobState's loop then finds no "job-state" and reports the
	// misleading "job-state not found", which pollAndVerify maps to a hard
	// CUPS_UNAVAILABLE/503 for a job that may have physically printed. Surfacing
	// the real status here lets callers fail loudly with the precise IPP code
	// instead of silently halting the print or returning an empty list.
	if err := checkIPPStatus(&resp); err != nil {
		return &resp, err
	}
	return &resp, nil
}

// checkIPPStatus returns nil when the IPP response status is in the success set,
// otherwise an error carrying the numeric + named status. The success set
// follows RFC 8011: successful-ok plus the two "ok with caveats" variants
// (ignored-or-substituted-attributes, conflicting-attributes) which still mean
// the operation succeeded.
func checkIPPStatus(resp *goipp.Message) error {
	st := goipp.Status(resp.Code)
	switch st {
	case goipp.StatusOk, goipp.StatusOkIgnoredOrSubstituted, goipp.StatusOkConflicting:
		return nil
	default:
		return fmt.Errorf("IPP error 0x%04x %s", uint16(resp.Code), st)
	}
}
