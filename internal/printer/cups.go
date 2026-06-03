package printer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/OpenPrinting/goipp"
)

// IPP job-state values (RFC 8011 §5.3.7).
const (
	JobPending          = 3
	JobPendingHeld      = 4
	JobProcessing       = 5
	JobProcessingStopped = 6
	JobCanceled         = 7
	JobAborted          = 8
	JobCompleted        = 9
)

// CUPSClient submits raw jobs via `lp -o raw` and polls job-state via IPP.
type CUPSClient struct {
	queue   string
	ippURL  string // http://localhost:631/printers/<queue>
	httpc   *http.Client
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

// Submit pipes the data (ZPL) to `lp -o raw` and returns the CUPS job id.
func (c *CUPSClient) Submit(ctx context.Context, data []byte, copies int) (int, error) {
	args := []string{"-d", c.queue, "-o", "raw"}
	if copies > 1 {
		args = append(args, "-n", strconv.Itoa(copies))
	}
	cmd := exec.CommandContext(ctx, "lp", args...)
	cmd.Stdin = bytes.NewReader(data)
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
	return &resp, nil
}
