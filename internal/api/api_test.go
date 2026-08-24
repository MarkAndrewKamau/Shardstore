package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MarkAndrewKamau/shardstore/internal/storage"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := storage.NewMemStore()
	ecParams := storage.ECParams{DataShards: 4, ParityShards: 2}
	objectStore := storage.NewObjectStore(store, ecParams, 1<<10)
	return New("test-node", logger, objectStore, true)
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_internal/health/check", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status field = %q, want %q", body.Status, "ok")
	}
	if body.Node != "test-node" {
		t.Errorf("node field = %q, want %q", body.Node, "test-node")
	}
}

func TestHealthMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_internal/health/check", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestRequestIDGeneratedAndEchoed(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_internal/health/check", nil))

	got := rec.Header().Get(requestIDHeader)
	if got == "" {
		t.Fatal("X-Request-Id header missing from response")
	}
	if len(got) != 16 {
		t.Errorf("request id length = %d, want 16 hex chars", len(got))
	}
}

func TestRequestIDHonoredFromClient(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(requestIDHeader, "client-supplied-id")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get(requestIDHeader); got != "client-supplied-id" {
		t.Errorf("X-Request-Id = %q, want %q", got, "client-supplied-id")
	}
}

func TestUnknownRoute(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestAccessLogEmitsRequestID(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	store := storage.NewMemStore()
	ecParams := storage.ECParams{DataShards: 4, ParityShards: 2}
	objectStore := storage.NewObjectStore(store, ecParams, 1<<10)
	srv := New("test-node", logger, objectStore, true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_internal/health/check", nil))

	if !strings.Contains(buf.String(), "http_request") {
		t.Fatalf("access log line missing, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "request_id=") {
		t.Errorf("access log missing request_id, got: %q", buf.String())
	}
}
