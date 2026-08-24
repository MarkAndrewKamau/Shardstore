// Package api hosts the external HTTP surface of shardstore.
//
// Phase 0 scope: health endpoint, request-ID plumbing, and structured
// access logging. S3 operations land in Phase 2.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MarkAndrewKamau/shardstore/internal/logging"
	"github.com/MarkAndrewKamau/shardstore/internal/storage"
)

const requestIDHeader = "X-Request-Id"

// Server exposes shardstore's HTTP handlers.
type Server struct {
	nodeID         string
	logger         *slog.Logger
	mux            *http.ServeMux
	objectStore    *storage.ObjectStore
	permissiveAuth bool
}

// New builds an API server for the given node.
func New(nodeID string, logger *slog.Logger, objectStore *storage.ObjectStore, permissiveAuth bool) *Server {
	s := &Server{
		nodeID:         nodeID,
		logger:         logger,
		mux:            http.NewServeMux(),
		objectStore:    objectStore,
		permissiveAuth: permissiveAuth,
	}
	s.mux.HandleFunc("GET /_internal/health/check", s.handleHealth)
	
	s.mux.HandleFunc("PUT /{bucket}", s.handleCreateBucket)
	s.mux.HandleFunc("DELETE /{bucket}", s.handleDeleteBucket)
	s.mux.HandleFunc("GET /{bucket}", s.handleListObjectsV2)
	s.mux.HandleFunc("HEAD /{bucket}", s.handleHeadBucket)
	s.mux.HandleFunc("PUT /{bucket}/{object}", s.handlePutObject)
	s.mux.HandleFunc("GET /{bucket}/{object}", s.handleGetObject)
	s.mux.HandleFunc("HEAD /{bucket}/{object}", s.handleHeadObject)
	s.mux.HandleFunc("DELETE /{bucket}/{object}", s.handleDeleteObject)
	
	// Multipart upload
	s.mux.HandleFunc("POST /{bucket}/{object}", s.handleMultipartOperations)
	
	return s
}

// Handler returns the full middleware-wrapped HTTP handler.
// requestID is outermost so every inner middleware (incl. accessLog) sees
// the ID-carrying request context.
func (s *Server) Handler() http.Handler {
	return s.requestID(s.accessLog(s.auth(s.mux)))
}

// auth middleware verifies SigV4 signatures unless permissiveAuth is enabled.
func (s *Server) auth(next http.Handler) http.Handler {
	if s.permissiveAuth {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health endpoint
		if r.URL.Path == "/_internal/health/check" {
			next.ServeHTTP(w, r)
			return
		}
		
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			s.writeXMLError(w, http.StatusForbidden, ErrCodeAccessDenied, "Missing Authorization header", r.URL.Path, r)
			return
		}
		
		// Basic validation - check if it looks like AWS4-HMAC-SHA256
		if !strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256 ") {
			s.writeXMLError(w, http.StatusForbidden, ErrCodeSignatureDoesNotMatch, "Invalid Authorization header format", r.URL.Path, r)
			return
		}
		
		// TODO: Full SigV4 verification with credential lookup
		// For now, we accept any valid-looking AWS4-HMAC-SHA256 header in non-permissive mode
		// Full implementation requires credential store (Phase 3)
		
		next.ServeHTTP(w, r)
	})
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

// writeXMLError writes an S3-style XML error response.
func (s *Server) writeXMLError(w http.ResponseWriter, status int, code, message, resource string, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	reqID := logging.RequestID(r.Context())
	errResp := S3ErrorResponse{
		Code:       code,
		Message:    message,
		Resource:   resource,
		RequestID:  reqID,
		HostID:     s.nodeID,
	}
	_ = xml.NewEncoder(w).Encode(errResp)
}

// writeXML writes an XML response.
func (s *Server) writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(v)
}

// parsePathValues extracts bucket and object from the request path.
func parsePathValues(p string) (bucket, object string) {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", ""
	}
	parts := strings.SplitN(p, "/", 3)
	if len(parts) >= 1 {
		bucket = parts[0]
	}
	if len(parts) >= 3 {
		object = parts[2]
	} else if len(parts) == 2 {
		object = parts[1]
	}
	return bucket, object
}

// validateBucketName validates S3 bucket naming rules.
func validateBucketName(name string) error {
	if name == "" {
		return errors.New("bucket name cannot be empty")
	}
	if len(name) < 3 || len(name) > 63 {
		return errors.New("bucket name must be between 3 and 63 characters")
	}
	// Check for valid characters
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			continue
		}
		return errors.New("bucket name can only contain lowercase letters, numbers, hyphens, and dots")
	}
	// Can't start or end with hyphen or dot
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") ||
		strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return errors.New("bucket name cannot start or end with hyphen or dot")
	}
	// No consecutive periods
	if strings.Contains(name, "..") {
		return errors.New("bucket name cannot contain consecutive periods")
	}
	return nil
}

// handleCreateBucket handles PUT /{bucket}
func (s *Server) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	if bucket == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket name cannot be empty", r.URL.Path, r)
		return
	}
	if err := validateBucketName(bucket); err != nil {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	// For now, just create a marker object to represent the bucket
	// In Phase 3, this will be handled by the metadata service
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	
	// Check if bucket already exists
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err == nil {
		s.writeXMLError(w, http.StatusConflict, ErrCodeBucketAlreadyOwnedByYou, "Bucket already exists", r.URL.Path, r)
		return
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	// Create empty bucket marker
	emptyReader := io.NopCloser(strings.NewReader(""))
	err = s.objectStore.PutObject(ctx, bucketMarkerID, emptyReader, 0)
	if err != nil {
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to create bucket", r.URL.Path, r)
		return
	}
	
	w.WriteHeader(http.StatusOK)
}

// handleDeleteBucket handles DELETE /{bucket}
func (s *Server) handleDeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	if bucket == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket name cannot be empty", r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	
	// Check if bucket exists
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	// Delete bucket marker
	err = s.objectStore.DeleteObject(ctx, bucketMarkerID)
	if err != nil {
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to delete bucket", r.URL.Path, r)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

// handleListObjectsV2 handles GET /{bucket}
func (s *Server) handleListObjectsV2(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	if bucket == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket name cannot be empty", r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	
	// Check if bucket exists
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	// Parse query parameters
	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	maxKeysStr := r.URL.Query().Get("max-keys")
	_ = r.URL.Query().Get("continuation-token")
	
	maxKeys := 1000
	if maxKeysStr != "" {
		if n, err := strconv.Atoi(maxKeysStr); err == nil && n > 0 {
			maxKeys = n
		}
	}
	
	// List all objects in the bucket
	allKeys, err := s.objectStore.ObjectShards(ctx, "")
	if err != nil {
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to list objects", r.URL.Path, r)
		return
	}
	
	// Build object list from shards
	objectMap := make(map[string]*Object)
	for _, key := range allKeys {
		objID := key.ObjectID
		if !strings.HasPrefix(objID, bucket+"/") {
			continue
		}
		if strings.HasPrefix(objID, "__bucket__/") {
			continue
		}
		if strings.HasPrefix(objID, "__multipart__/") {
			continue
		}
		
		objKey := strings.TrimPrefix(objID, bucket+"/")
		
		if prefix != "" && !strings.HasPrefix(objKey, prefix) {
			continue
		}
		
		if delimiter != "" {
			idx := strings.Index(objKey[len(prefix):], delimiter)
			if idx >= 0 {
				_ = objKey[:len(prefix)+idx+1]
			}
		}
		
		// Get object metadata from marker
		_, size, err := s.objectStore.GetObject(ctx, objID)
		if err != nil {
			continue
		}
		
		if _, ok := objectMap[objKey]; !ok {
			objectMap[objKey] = &Object{
				Key:          objKey,
				LastModified: time.Now(),
				ETag:         fmt.Sprintf("\"%x\"", size),
				Size:         size,
				StorageClass: "STANDARD",
				Owner:        Owner{ID: s.nodeID, DisplayName: s.nodeID},
			}
		}
	}
	
	// Convert to slice
	var contents []Object
	for _, obj := range objectMap {
		contents = append(contents, *obj)
		if len(contents) >= maxKeys {
			break
		}
	}
	
	result := ListObjectsV2Result{
		Name:           bucket,
		Prefix:         prefix,
		KeyCount:       len(contents),
		MaxKeys:        maxKeys,
		Delimiter:      delimiter,
		IsTruncated:    len(contents) >= maxKeys,
		Contents:       contents,
		CommonPrefixes: []CommonPrefix{},
	}
	
	s.writeXML(w, http.StatusOK, result)
}

// handleHeadBucket handles HEAD /{bucket}
func (s *Server) handleHeadBucket(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	if bucket == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket name cannot be empty", r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	w.WriteHeader(http.StatusOK)
}

// handlePutObject handles PUT /{bucket}/{object}
func (s *Server) handlePutObject(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	_, object := parsePathValues(r.URL.Path)
	
	if bucket == "" || object == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket and object required", r.URL.Path, r)
		return
	}
	
	// Check if bucket exists
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	// Get content length
	contentLength := r.ContentLength
	if contentLength < 0 {
		s.writeXMLError(w, http.StatusLengthRequired, ErrCodeMissingContentLength, "Content-Length required", r.URL.Path, r)
		return
	}
	
	// Object ID is bucket/object
	objectID := bucket + "/" + object
	
	// Read body and store
	err = s.objectStore.PutObject(ctx, objectID, r.Body, contentLength)
	if err != nil {
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to put object", r.URL.Path, r)
		return
	}
	
	// Return ETag
	etag := fmt.Sprintf("\"%x\"", contentLength)
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

// handleGetObject handles GET /{bucket}/{object}
func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	_, object := parsePathValues(r.URL.Path)
	
	if bucket == "" || object == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket and object required", r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	objectID := bucket + "/" + object
	reader, size, err := s.objectStore.GetObject(ctx, objectID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchKey, "Object does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to get object", r.URL.Path, r)
		return
	}
	defer reader.Close()
	
	// Handle Range header
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		// Parse range header (simple implementation)
		// Format: bytes=start-end
		if strings.HasPrefix(rangeHeader, "bytes=") {
			rangeSpec := strings.TrimPrefix(rangeHeader, "bytes=")
			parts := strings.Split(rangeSpec, "-")
			if len(parts) == 2 {
				start, _ := strconv.ParseInt(parts[0], 10, 64)
				end, _ := strconv.ParseInt(parts[1], 10, 64)
				if end == 0 {
					end = size - 1
				}
				if start >= 0 && end < size && start <= end {
					// Seek to start
					// For now, we'll just return the full object
					// A full implementation would use io.SectionReader
				}
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", 0, size-1, size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", size))
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, reader)
}

// handleHeadObject handles HEAD /{bucket}/{object}
func (s *Server) handleHeadObject(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	_, object := parsePathValues(r.URL.Path)
	
	if bucket == "" || object == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket and object required", r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	objectID := bucket + "/" + object
	_, size, err := s.objectStore.GetObject(ctx, objectID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchKey, "Object does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to get object", r.URL.Path, r)
		return
	}
	
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", size))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
}

// handleDeleteObject handles DELETE /{bucket}/{object}
func (s *Server) handleDeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	_, object := parsePathValues(r.URL.Path)
	
	if bucket == "" || object == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket and object required", r.URL.Path, r)
		return
	}
	
	ctx := r.Context()
	bucketMarkerID := fmt.Sprintf("__bucket__/%s", bucket)
	_, _, err := s.objectStore.GetObject(ctx, bucketMarkerID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchBucket, "Bucket does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to check bucket", r.URL.Path, r)
		return
	}
	
	objectID := bucket + "/" + object
	err = s.objectStore.DeleteObject(ctx, objectID)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			s.writeXMLError(w, http.StatusNotFound, ErrCodeNoSuchKey, "Object does not exist", r.URL.Path, r)
			return
		}
		s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to delete object", r.URL.Path, r)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

// handleMultipartOperations handles POST /{bucket}/{object} for multipart uploads
func (s *Server) handleMultipartOperations(w http.ResponseWriter, r *http.Request) {
	bucket, _ := parsePathValues(r.URL.Path)
	_, object := parsePathValues(r.URL.Path)
	
	if bucket == "" || object == "" {
		s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Bucket and object required", r.URL.Path, r)
		return
	}
	
	// Check operation type from query parameters
	_ = r.URL.Query().Get("uploads")
	uploadsParam := r.URL.Query().Get("uploads")
	uploadID := r.URL.Query().Get("uploadId")
	partNumberStr := r.URL.Query().Get("partNumber")
	
	ctx := r.Context()
	
	if uploadsParam != "" && r.Method == "GET" {
		// List multipart uploads - not implemented yet
		s.writeXMLError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "ListMultipartUploads not implemented", r.URL.Path, r)
		return
	}
	
	if uploadID == "" && partNumberStr == "" && r.Method == "POST" {
		// Create multipart upload
		uploadsParam := r.URL.Query().Get("uploads")
		if uploadsParam != "" {
			// List multipart uploads
			s.writeXMLError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "ListMultipartUploads not implemented", r.URL.Path, r)
			return
		}
		// Initiate multipart upload
		objectID := bucket + "/" + object
		uploadID := generateUploadID()
		
		// Store upload ID as a marker
		uploadMarkerID := fmt.Sprintf("__multipart__/%s/%s", objectID, uploadID)
		emptyReader := io.NopCloser(strings.NewReader(""))
		err := s.objectStore.PutObject(ctx, uploadMarkerID, emptyReader, 0)
		if err != nil {
			s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to initiate multipart upload", r.URL.Path, r)
			return
		}
		
		result := CreateMultipartUploadResult{
			Bucket:   bucket,
			Key:      object,
			UploadID: uploadID,
		}
		s.writeXML(w, http.StatusOK, result)
		return
	}
	
	if uploadID != "" && partNumberStr != "" && r.Method == "PUT" {
		// Upload part
		partNumber, err := strconv.Atoi(partNumberStr)
		if err != nil || partNumber < 1 {
			s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Invalid part number", r.URL.Path, r)
			return
		}
		
		objectID := bucket + "/" + object
		partMarkerID := fmt.Sprintf("__multipart__/%s/%s/part/%d", objectID, uploadID, partNumber)
		
		contentLength := r.ContentLength
		if contentLength < 0 {
			s.writeXMLError(w, http.StatusLengthRequired, ErrCodeMissingContentLength, "Content-Length required", r.URL.Path, r)
			return
		}
		
		// Enforce 5 MiB minimum for non-final parts
		// We don't know if this is the final part, so we enforce for all parts
		// In a full implementation, we'd track this
		if contentLength < 5*1024*1024 {
			s.writeXMLError(w, http.StatusBadRequest, ErrCodeEntityTooSmall, "Part size must be at least 5 MiB", r.URL.Path, r)
			return
		}
		
		err = s.objectStore.PutObject(ctx, partMarkerID, r.Body, contentLength)
		if err != nil {
			s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to upload part", r.URL.Path, r)
			return
		}
		
		etag := fmt.Sprintf("\"%x\"", contentLength)
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		return
	}
	
	if uploadID != "" && r.Method == "GET" {
		// List parts
		s.writeXMLError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "ListParts not implemented", r.URL.Path, r)
		return
	}
	
	if uploadID != "" && r.Method == "POST" {
		action := r.URL.Query().Get("action")
		if action == "complete" {
			// Complete multipart upload
			body, err := io.ReadAll(r.Body)
			if err != nil {
				s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to read request body", r.URL.Path, r)
				return
			}
			
			var completeReq CompleteMultipartUpload
			if err := xml.Unmarshal(body, &completeReq); err != nil {
				s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Invalid XML", r.URL.Path, r)
				return
			}
			
			objectID := bucket + "/" + object
			
			// Read all parts in order and concatenate
			var fullData strings.Builder
			for _, part := range completeReq.Parts {
				partMarkerID := fmt.Sprintf("__multipart__/%s/%s/part/%d", objectID, uploadID, part.PartNumber)
				reader, _, err := s.objectStore.GetObject(ctx, partMarkerID)
				if err != nil {
					s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to read part", r.URL.Path, r)
					return
				}
				data, _ := io.ReadAll(reader)
				fullData.Write(data)
				reader.Close()
			}
			
			// Store the complete object
			combinedData := fullData.String()
			err = s.objectStore.PutObject(ctx, objectID, strings.NewReader(combinedData), int64(len(combinedData)))
			if err != nil {
				s.writeXMLError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to complete multipart upload", r.URL.Path, r)
				return
			}
			
			// Clean up part markers
			for _, part := range completeReq.Parts {
				partMarkerID := fmt.Sprintf("__multipart__/%s/%s/part/%d", objectID, uploadID, part.PartNumber)
				s.objectStore.DeleteObject(ctx, partMarkerID)
			}
			
			// Delete upload marker
			uploadMarkerID := fmt.Sprintf("__multipart__/%s/%s", objectID, uploadID)
			s.objectStore.DeleteObject(ctx, uploadMarkerID)
			
			result := CompleteMultipartUploadResult{
				Location: fmt.Sprintf("/%s/%s", bucket, object),
				Bucket:   bucket,
				Key:      object,
				ETag:     fmt.Sprintf("\"%x\"", len(combinedData)),
			}
			s.writeXML(w, http.StatusOK, result)
			return
		}
		if action == "abort" {
			// Abort multipart upload
			objectID := bucket + "/" + object
			uploadMarkerID := fmt.Sprintf("__multipart__/%s/%s", objectID, uploadID)
			
			// List and delete all parts
			// For simplicity, we'll just delete the upload marker
			s.objectStore.DeleteObject(ctx, uploadMarkerID)
			
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	
	s.writeXMLError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "Invalid multipart operation", r.URL.Path, r)
}

// CompleteMultipartUpload is the request body for completing multipart upload
type CompleteMultipartUpload struct {
	XMLName xml.Name              `xml:"CompleteMultipartUpload"`
	Parts   []CompleteMultipartPart `xml:"Part"`
}

type CompleteMultipartPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

func generateUploadID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000Z")
	}
	return hex.EncodeToString(b[:])
}