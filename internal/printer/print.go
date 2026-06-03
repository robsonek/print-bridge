package printer

import (
	"context"
	"errors"
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
			// #6: CUPS no longer has the job in its history (purged/evicted). For a
			// job WE already submitted, "gone" almost certainly means it completed.
			// The IPP queue is no longer authoritative -> trust the hardware: route
			// to the ~HS verify() (best-effort physical check) instead of failing
			// the recovery path with a hard, retryable CUPS_UNAVAILABLE that would
			// false-requeue (double print) or false-alert a label that printed.
			if errors.Is(err, ErrJobGone) {
				return p.verify(ctx, jobID)
			}
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeCUPSUnavailable, "job poll failed: "+err.Error(), 503)
		}
		// RFC 8011 §5.3.7: only completed / canceled / aborted are TERMINATING
		// states. processing-stopped (6) is non-terminal => keep polling: (#1) the
		// printer halted mid-job (out of paper / jam / pause) and CUPS will resume
		// it to processing->completed once the fault clears; aborting would porzucic
		// an odzyskiwalny job and risk a duplicate on retry.
		switch state {
		case JobCompleted:
			return p.verify(ctx, jobID)
		case JobCanceled:
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeInvalidZPL, "job canceled by CUPS", 422)
		case JobAborted:
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeCUPSUnavailable, "job aborted by CUPS", 503)
		case JobPendingHeld:
			// #9: IPP pending-held (4) is non-terminal but, unlike processing-stopped,
			// it will NOT resume on its own — it needs an explicit operator release
			// (Release-Job / cupsenable). Polling it out wastes the whole confirm
			// budget (~30s) and then returns a misleading PRINT_TIMEOUT. Return fast
			// with a descriptive QUEUE_PAUSED (retryable: a later retry / resume-by-key
			// finishes the job once released) carrying the IPP state + job id so the
			// operator gets an actionable signal instead of a generic timeout.
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeQueuePaused,
				"job held by CUPS (pending-held); requires operator release", 503).
				WithDetail("ipp_job_state", state).
				WithDetail("cups_job_id", id)
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

	// #2: split two materially different ~HS probe outcomes that used to share
	// one branch.
	//
	// err != nil  => TRANSPORT failure (dial/write/read fail): the printer became
	// UNREACHABLE between CUPS JobCompleted (bytes flushed to the socket buffer)
	// and this ~HS probe. The socket:9100 backend gives CUPS no physical-print
	// back-channel, so the physical print state is UNKNOWN -> we must NOT claim
	// "printed". Return retryable PRINTER_OFFLINE; resume-by-key + a completed
	// CUPS job mean a retry will re-verify via ~HS (without reprinting) once the
	// printer is back.
	if err != nil {
		return Result{CUPSJobID: id}, apierr.New(apierr.CodePrinterOffline,
			"printer unreachable during ~HS verification: "+err.Error(), 503)
	}

	// err == nil && !ok => the printer ANSWERED but the ~HS reply was unparseable
	// / unsupported. The printer is alive and reachable, it just doesn't speak ~HS
	// intelligibly -> graceful degrade to best-effort "printed" (as designed).
	if !ok {
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
