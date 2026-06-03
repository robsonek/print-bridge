package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/robsonek/print-bridge/internal/apierr"
	"github.com/robsonek/print-bridge/internal/printer"
)

// StoreRecord mirrors idempotency.Record without importing it (decoupling for tests).
type StoreRecord struct {
	ResponseJSON string
	CUPSJobID    string
	Terminal     bool
}

type Store interface {
	Get(key string) (StoreRecord, bool, error)
	SavePending(key, cupsJobID string) error
	SaveTerminal(key, responseJSON string) error
}

type PrintService interface {
	Print(ctx context.Context, data []byte, copies int) (printer.Result, *apierr.Error)
	ResumeJob(ctx context.Context, jobID int) (printer.Result, *apierr.Error)
}

type Handlers struct {
	Printer PrintService
	Store   Store
	KeyLock *KeyLock
	Health  func(context.Context) (int, any)
	Updater func(tag string) error

	// ConfirmTimeout (#27) is the server-side upper bound for the WHOLE print
	// operation (exec lp + every JobState IPP round-trip + verify), not just the
	// poll loop's iteration count. Without it, exec lp and each JobState inherit
	// only r.Context() (canceled solely on client disconnect/handler return), so a
	// hung cupsd `lp` held open by a non-timing-out client has no upper bound. We
	// derive a child context with this deadline before calling Print/ResumeJob.
	// It MUST exceed the healthy poll loop's real worst case
	// (~ConfirmTimeoutPolls × (ippTimeout + PollInterval)) so a slow-but-healthy
	// print is never cut off into a false PRINT_TIMEOUT; main sizes it accordingly.
	ConfirmTimeout time.Duration
}

// printContext derives a child of the request context bounded by ConfirmTimeout
// (#27). When ConfirmTimeout is 0 (unset) it falls back to the request context so
// behavior is unchanged. The caller MUST invoke the returned cancel func.
func (h *Handlers) printContext(r *http.Request) (context.Context, context.CancelFunc) {
	if h.ConfirmTimeout <= 0 {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), h.ConfirmTimeout)
}

// maxBodyBytes caps the request body for body-reading handlers (#17). A thermal
// label is tens of KB; a merged multi-parcel PDF is a few MB. 20 MB leaves ample
// headroom while preventing a base64-amplification DoS (decoded payload then
// expanded again by poppler -> ZPL) from exhausting a small warehouse VM's RAM.
const maxBodyBytes = 20 << 20 // 20 MB

type printJobRequest struct {
	LabelBase64 string `json:"label_base64"`
	PDFBase64   string `json:"pdf_base64"`
	Format      string `json:"format"`
	Copies      int    `json:"copies"`
	ExternalRef string `json:"external_reference"`
}

func (h *Handlers) PrintJobs(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, apierr.New(apierr.CodeInvalidRequest, "Idempotency-Key header required", http.StatusBadRequest))
		return
	}

	// Per-key lock (#3,#5): serialize the whole Get -> resume/print -> persist
	// flow for concurrent requests sharing this Idempotency-Key, so two parallel
	// requests cannot both fall through to a fresh Submit and double-print.
	unlock := h.KeyLock.Lock(key)
	defer unlock()

	// Resume-by-key. Distinguish store-error (#13: must NOT print, refuse and let
	// the caller retry) from a genuine miss (safe to do a fresh print).
	rec, found, err := h.Store.Get(key)
	if err != nil {
		log.Printf("idempotency Get failed for key %q: %v", key, err)
		// Pending row may already carry a submitted job; a fresh print could
		// duplicate it. Retryable so Laravel re-tries instead of resubmitting.
		writeError(w, apierr.New(apierr.CodeBridgeRestarting, "idempotency store unavailable, retry", http.StatusServiceUnavailable))
		return
	}
	if found {
		if rec.Terminal {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(rec.ResponseJSON))
			return
		}
		// Pending: a job was already submitted to CUPS for this key. Resume it
		// instead of resubmitting. If the stored job id is missing/unparsable
		// (corruption — unreachable in normal flow), refuse with a retryable
		// error rather than risk a duplicate print (#13).
		jobID, cerr := strconv.Atoi(rec.CUPSJobID)
		if rec.CUPSJobID == "" || cerr != nil {
			log.Printf("pending record with unusable cups_job_id for key %q: %q", key, rec.CUPSJobID)
			writeError(w, apierr.New(apierr.CodeBridgeRestarting, "pending job has no resumable cups_job_id", http.StatusServiceUnavailable))
			return
		}
		ctx, cancel := h.printContext(r) // #27: bound the resume/poll budget
		defer cancel()
		res, perr := h.Printer.ResumeJob(ctx, jobID)
		h.persistPending(key, res)
		h.finish(w, key, res, perr)
		return
	}

	// #17: cap the body BEFORE decoding (only reached on the fresh-print path; the
	// resume-by-key branch above never reads the body). MaxBytesReader makes Decode
	// fail once the limit is exceeded, which the existing error branch maps to 400.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req printJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apierr.New(apierr.CodeInvalidRequest, "invalid JSON body", http.StatusBadRequest))
		return
	}
	b64 := req.LabelBase64
	if b64 == "" {
		b64 = req.PDFBase64
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		writeError(w, apierr.New(apierr.CodeInvalidRequest, "label_base64/pdf_base64 missing or not base64", http.StatusBadRequest))
		return
	}
	copies := req.Copies
	if copies < 1 {
		copies = 1
	}

	ctx, cancel := h.printContext(r) // #27: bound exec lp + poll/verify with a budget
	defer cancel()
	res, perr := h.Printer.Print(ctx, data, copies)
	// Persist the cups job id whenever Print surfaced one — including post-submit
	// error paths (#4,#11). The Result carries the job id even on a retryable error
	// after a successful Submit, so a retry with this key RESUMES the existing job
	// (ResumeJob above) instead of resubmitting => no duplicate label.
	//
	// ACCEPTED RESIDUAL: a sub-second crash window remains between the lp Submit
	// inside Print() and this SavePending. It cannot be closed without a pre-submit
	// marker (a much larger change); this fix already eliminates the 30s polling
	// window and the concurrency window, which were the dominant duplicate vectors.
	h.persistPending(key, res)
	h.finish(w, key, res, perr)
}

// persistPending records the CUPS job id for resume-by-key when present.
func (h *Handlers) persistPending(key string, res printer.Result) {
	if res.CUPSJobID == "" {
		return
	}
	if err := h.Store.SavePending(key, res.CUPSJobID); err != nil {
		log.Printf("idempotency SavePending key=%q job=%q failed: %v", key, res.CUPSJobID, err)
	}
}

func (h *Handlers) finish(w http.ResponseWriter, key string, res printer.Result, perr *apierr.Error) {
	if perr != nil {
		writeError(w, perr)
		return
	}
	body, _ := json.Marshal(res)
	// Log (don't fail) a SaveTerminal error (#25): the physical print already
	// succeeded; a retry will resume the (completed) job and replay rather than
	// reprint, so returning success here is correct.
	if err := h.Store.SaveTerminal(key, string(body)); err != nil {
		log.Printf("idempotency SaveTerminal key=%q failed: %v", key, err)
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) HealthHandler(w http.ResponseWriter, r *http.Request) {
	status, body := h.Health(r.Context())
	writeJSON(w, status, body)
}

type updateRequest struct {
	Tag string `json:"tag"`
}

func (h *Handlers) AdminUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes) // #17: DoS guard
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apierr.New(apierr.CodeInvalidRequest, "invalid JSON", http.StatusBadRequest))
		return
	}
	if err := h.Updater(req.Tag); err != nil {
		writeError(w, apierr.New(apierr.CodeInvalidRequest, err.Error(), http.StatusUnprocessableEntity))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "updating", "tag": req.Tag})
}
