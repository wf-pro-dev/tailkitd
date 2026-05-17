package helpers

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	tailkit "github.com/wf-pro-dev/tailkit"
	"go.uber.org/zap"
)

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	return r.ResponseWriter.Write(data)
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) Push(target string, opts *http.PushOptions) error {
	pusher, ok := r.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (r *responseRecorder) ReadFrom(src io.Reader) (int64, error) {
	readerFrom, ok := r.ResponseWriter.(io.ReaderFrom)
	if ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

func RequestLogger(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := uuid.New().String()
			r = r.WithContext(WithRequestID(r.Context(), requestID))

			start := time.Now()
			rec := &responseRecorder{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(rec, r)

			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rec.status),
				zap.Int64("duration_ms", time.Since(start).Milliseconds()),
				zap.String("request_id", requestID),
			}
			if caller, ok := tailkit.CallerFromContext(r.Context()); ok && caller.Hostname != "" {
				fields = append(fields, zap.String("caller", caller.Hostname))
			}

			switch {
			case rec.status >= 500:
				logger.Error("request failed", fields...)
			case rec.status >= 400:
				logger.Warn("request rejected", fields...)
			case isStateChangingMethod(r.Method):
				logger.Info("request completed", fields...)
			case isLowValueReadPath(r):
				logger.Debug("request completed", fields...)
			default:
				logger.Info("request completed", fields...)
			}
		})
	}
}

func WithRequestLogFields(ctx context.Context, fields []zap.Field) []zap.Field {
	if requestID, ok := RequestIDFromContext(ctx); ok {
		fields = append(fields, zap.String("request_id", requestID))
	}
	return fields
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

func isLowValueReadPath(r *http.Request) bool {
	path := r.URL.Path
	switch {
	case path == "/health":
		return true
	case strings.HasSuffix(path, "/available"):
		return true
	case strings.HasSuffix(path, "/config"):
		return true
	case strings.HasSuffix(path, "/stream"):
		return true
	case strings.HasPrefix(path, "/exec/jobs/"):
		return true
	}

	if r.Method == http.MethodGet && r.URL.Query().Get("follow") == "true" {
		return true
	}

	return false
}
