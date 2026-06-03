package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

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
	Health  func(context.Context) (int, any)
	Updater func(tag string) error
}

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

	// Resume-by-key.
	if rec, found, err := h.Store.Get(key); err == nil && found {
		if rec.Terminal {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(rec.ResponseJSON))
			return
		}
		if rec.CUPSJobID != "" {
			if jobID, cerr := strconv.Atoi(rec.CUPSJobID); cerr == nil {
				res, perr := h.Printer.ResumeJob(r.Context(), jobID)
				h.finish(w, key, res, perr)
				return
			}
		}
	}

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

	res, perr := h.Printer.Print(r.Context(), data, copies)
	if perr == nil {
		// Persist cups job id for resume before declaring terminal.
		_ = h.Store.SavePending(key, res.CUPSJobID)
	}
	h.finish(w, key, res, perr)
}

func (h *Handlers) finish(w http.ResponseWriter, key string, res printer.Result, perr *apierr.Error) {
	if perr != nil {
		writeError(w, perr)
		return
	}
	body, _ := json.Marshal(res)
	_ = h.Store.SaveTerminal(key, string(body))
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
