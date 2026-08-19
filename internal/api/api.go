// Package api hosts the external HTTP surface of shardstore.
//
// Phase 0 scope: health endpoint, request-ID plumbing, and structured
// access logging. S3 operations land in Phase 2.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/MarkAndrewKamau/shardstore/internal/logging"
)

const requestIDHeader = "X-Request-Id"

// Server exposes shardstore's HTTP handlers.
type Server struct {
	nodeID string
	logger *slog.Logger
	mux    *http.ServeMux
}

// New builds an API server for the given node.
func New(nodeID string, logger *slog.Logger) *Server {
	s := &Server{
		nodeID: nodeID,
		logger: logger,
		mux:    http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	return s
}

// Handler returns the full middleware-wrapped HTTP handler.
// requestID is outermost so every inner middleware (incl. accessLog) sees
// the ID-carrying request context.
func (s *Server) Handler() http.Handler {
	return s.requestID(s.accessLog(s.mux))
}

// requestID ensures every request carries an ID: it honors an incoming
// X-Request-Id header and otherwise generates one.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// accessLog emits one structured line per request.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.logger.Info("http_request",
			"request_id", logging.RequestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type healthResponse struct {
	Status string `json:"status"`
	Node   string `json:"node"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Node: s.nodeID})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000Z")
	}
	return hex.EncodeToString(b[:])
}
