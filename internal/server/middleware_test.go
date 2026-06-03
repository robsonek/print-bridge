package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}

func TestHealthBypassesAuth(t *testing.T) {
	h := TokenAuth("secret", okHandler())
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("/health should bypass auth, got %d", rec.Code)
	}
}

func TestMissingTokenRejected(t *testing.T) {
	h := TokenAuth("secret", okHandler())
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("missing token => 401, got %d", rec.Code)
	}
}

func TestWrongTokenForbidden(t *testing.T) {
	h := TokenAuth("secret", okHandler())
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", nil)
	req.Header.Set("X-Print-Token", "nope")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("wrong token => 403, got %d", rec.Code)
	}
}

func TestCorrectTokenPasses(t *testing.T) {
	h := TokenAuth("secret", okHandler())
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", nil)
	req.Header.Set("X-Print-Token", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("correct token => pass, got %d", rec.Code)
	}
}
