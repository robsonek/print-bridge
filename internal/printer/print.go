package printer

import (
	"context"
	"strconv"
	"time"

	"github.com/robsonek/print-bridge/internal/apierr"
)

type Reachability interface {
	Reachable(context.Context) (bool, error)
	QueuePaused(context.Context) (bool, error)
}
type Submitter interface {
	Submit(ctx context.Context, data []byte, copies int) (int, error)
}
type Poller interface {
	JobState(ctx context.Context, jobID int) (int, error)
}
type Prober interface {
	HostStatus(context.Context) (HostStatus, bool, error)
}
type Renderer interface {
	PDFToZPL(context.Context, []byte) ([]byte, error)
}

// Printer orchestrates: sniff -> precheck -> prepare -> submit -> poll -> verify.
type Printer struct {
	Reach               Reachability
	Sub                 Submitter
	Poll                Poller
	Probe               Prober
	Render              Renderer
	PollInterval        time.Duration
	ConfirmTimeoutPolls int
}

type Result struct {
	Status    string `json:"status"`
	CUPSJobID string `json:"cups_job_id"`
}

func (p *Printer) Print(ctx context.Context, data []byte, copies int) (Result, *apierr.Error) {
	switch Sniff(data) {
	case FormatZPL:
		// passthrough
	case FormatPDF:
		zpl, err := p.Render.PDFToZPL(ctx, data)
		if err != nil {
			return Result{}, apierr.New(apierr.CodeInvalidPDF, "PDF render failed: "+err.Error(), 422)
		}
		data = zpl
	default:
		return Result{}, apierr.New(apierr.CodeUnsupportedFormat, "payload is neither PDF nor ZPL", 422)
	}

	if ok, err := p.Reach.Reachable(ctx); err != nil || !ok {
		return Result{}, apierr.New(apierr.CodePrinterOffline, "printer not reachable on socket :9100", 503)
	}
	if paused, err := p.Reach.QueuePaused(ctx); err == nil && paused {
		return Result{}, apierr.New(apierr.CodeQueuePaused, "CUPS queue is paused/disabled", 503)
	}

	jobID, err := p.Sub.Submit(ctx, data, copies)
	if err != nil {
		return Result{}, apierr.New(apierr.CodeCUPSUnavailable, "lp submit failed: "+err.Error(), 503)
	}

	return p.pollAndVerify(ctx, jobID)
}

// pollAndVerify is exported-for-resume via ResumeJob below; shared logic.
//
// The job has already been submitted to CUPS by the time we get here, so EVERY
// post-submit error path returns Result{CUPSJobID: id} populated (#4,#11). The
// handler persists that id (SavePending) regardless of the error, so a retry with
// the same Idempotency-Key resumes this job instead of resubmitting => no
// duplicate physical label.
func (p *Printer) pollAndVerify(ctx context.Context, jobID int) (Result, *apierr.Error) {
	id := strconv.Itoa(jobID)
	for i := 0; i < p.ConfirmTimeoutPolls; i++ {
		state, err := p.Poll.JobState(ctx, jobID)
		if err != nil {
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeCUPSUnavailable, "job poll failed: "+err.Error(), 503)
		}
		switch state {
		case JobCompleted:
			return p.verify(ctx, jobID)
		case JobCanceled:
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeInvalidZPL, "job canceled by CUPS", 422)
		case JobAborted, JobProcessingStopped:
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeCUPSUnavailable, "job aborted/stopped", 503)
		}
		if p.PollInterval > 0 {
			select {
			case <-ctx.Done():
				return Result{CUPSJobID: id}, apierr.New(apierr.CodePrintTimeout, "context canceled while polling", 503)
			case <-time.After(p.PollInterval):
			}
		}
	}
	return Result{CUPSJobID: id}, apierr.New(apierr.CodePrintTimeout, "job did not complete within confirm timeout", 503)
}

func (p *Printer) verify(ctx context.Context, jobID int) (Result, *apierr.Error) {
	hs, ok, err := p.Probe.HostStatus(ctx)
	id := strconv.Itoa(jobID)
	if err != nil || !ok {
		// Graceful degrade: printer doesn't speak ~HS -> best-effort printed.
		return Result{Status: "printed", CUPSJobID: id}, nil
	}
	switch {
	case hs.PaperOut:
		return Result{CUPSJobID: id}, apierr.New(apierr.CodeOutOfPaper, "printer reports media-empty (~HS)", 503)
	case hs.Paused:
		return Result{CUPSJobID: id}, apierr.New(apierr.CodeQueuePaused, "printer paused (~HS)", 503)
	case !hs.Healthy():
		return Result{CUPSJobID: id}, apierr.New(apierr.CodePrinterOffline, "printer fault (~HS): "+hs.Raw, 503)
	}
	return Result{Status: "printed", CUPSJobID: id}, nil
}

// ResumeJob continues polling an already-submitted job (resume-by-key path).
func (p *Printer) ResumeJob(ctx context.Context, jobID int) (Result, *apierr.Error) {
	return p.pollAndVerify(ctx, jobID)
}
