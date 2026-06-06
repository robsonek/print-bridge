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

	// Scale the confirm budget by label count: a multi-parcel job prints serially
	// and would false-time-out on the base budget while still printing.
	return p.pollAndVerify(ctx, jobID, p.ConfirmTimeoutPolls*labelCount(data))
}

// pollAndVerify is exported-for-resume via ResumeJob below; shared logic.
//
// The job has already been submitted to CUPS by the time we get here, so EVERY
// post-submit error path returns Result{CUPSJobID: id} populated (#4,#11). The
// handler persists that id (SavePending) regardless of the error, so a retry with
// the same Idempotency-Key resumes this job instead of resubmitting => no
// duplicate physical label.
func (p *Printer) pollAndVerify(ctx context.Context, jobID, maxPolls int) (Result, *apierr.Error) {
	id := strconv.Itoa(jobID)
	for i := 0; i < maxPolls; i++ {
		state, err := p.Poll.JobState(ctx, jobID)
		if err != nil {
			// #6: CUPS no longer has the job in its history (purged/evicted). For a
			// job WE already submitted, "gone" almost certainly means it completed.
			// The IPP queue is no longer authoritative -> trust the hardware: route
			// to the ~HS verify() (best-effort physical check) instead of failing
			// the recovery path with a hard, retryable CUPS_UNAVAILABLE that would
			// false-requeue (double print) or false-alert a label that printed.
			if errors.Is(err, ErrJobGone) {
				return p.verify(ctx, jobID, maxPolls-i-1)
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
			return p.verify(ctx, jobID, maxPolls-i-1)
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

// verify confirms the PHYSICAL print after CUPS reports the job done. With the
// paced LPD backend a CUPS job completes when the data is DELIVERED (~3 s),
// while the engine keeps printing for N×~5 s — so verify polls ~HS until the
// receive buffer and the running batch are drained (Draining()==false), within
// the budget of confirm polls left over from the job-state loop.
//
// #2 (amended for drain): a TRANSPORT failure of one probe no longer returns an
// immediate PRINTER_OFFLINE — the single-threaded print-server may simply be
// busy printing (hardware finding: "timeout during print means busy, not
// down"), so the probe retries within the budget. Only a budget exhausted
// without a single ~HS answer is reported as PRINTER_OFFLINE; resume-by-key +
// a completed CUPS job mean a retry re-verifies (without reprinting).
func (p *Printer) verify(ctx context.Context, jobID, budget int) (Result, *apierr.Error) {
	id := strconv.Itoa(jobID)
	if budget < 1 {
		budget = 1
	}
	var lastErr error
	answered := false
	for i := 0; i < budget; i++ {
		hs, ok, err := p.Probe.HostStatus(ctx)
		switch {
		case err != nil:
			lastErr = err // busy or unreachable — retry within budget

		// The printer ANSWERED but the ~HS reply was unparseable/unsupported.
		// It is alive and reachable, it just doesn't speak ~HS intelligibly ->
		// graceful degrade to best-effort "printed" (as designed).
		case !ok:
			return Result{Status: "printed", CUPSJobID: id}, nil

		case hs.PaperOut:
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeOutOfPaper, "printer reports media-empty (~HS)", 503)
		case hs.Paused:
			return Result{CUPSJobID: id}, apierr.New(apierr.CodeQueuePaused, "printer paused (~HS)", 503)
		case hs.HeadOpen:
			// MED #10 closed: head/cover open lives in ~HS string 2 and used to
			// be invisible (false "printed" with the head physically open).
			return Result{CUPSJobID: id}, apierr.New(apierr.CodePrinterOffline, "printer head/cover open (~HS)", 503)
		case !hs.Healthy():
			return Result{CUPSJobID: id}, apierr.New(apierr.CodePrinterOffline, "printer fault (~HS): "+hs.Raw, 503)

		// Drained: no formats waiting, no labels remaining -> physically printed.
		case !hs.Draining():
			return Result{Status: "printed", CUPSJobID: id}, nil

		default:
			answered = true // healthy, still printing the batch
		}
		if i < budget-1 && p.PollInterval > 0 {
			select {
			case <-ctx.Done():
				return Result{CUPSJobID: id}, apierr.New(apierr.CodePrintTimeout, "context canceled while verifying", 503)
			case <-time.After(p.PollInterval):
			}
		}
	}
	if !answered && lastErr != nil {
		return Result{CUPSJobID: id}, apierr.New(apierr.CodePrinterOffline,
			"printer unreachable during ~HS verification: "+lastErr.Error(), 503)
	}
	return Result{CUPSJobID: id}, apierr.New(apierr.CodePrintTimeout,
		"labels still printing (~HS draining) at confirm budget; retry with the same Idempotency-Key re-verifies", 503)
}

// ResumeJob continues polling an already-submitted job (resume-by-key path). It
// uses the base budget: on a retry the job has usually finished printing during
// the interval, so JobState returns terminal almost immediately.
func (p *Printer) ResumeJob(ctx context.Context, jobID int) (Result, *apierr.Error) {
	return p.pollAndVerify(ctx, jobID, p.ConfirmTimeoutPolls)
}
