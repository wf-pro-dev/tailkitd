package helpers

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestRequestLoggerPreservesFlusher(t *testing.T) {
	t.Parallel()

	flushed := false
	handler := RequestLogger(zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not implement http.Flusher")
		}
		flusher.Flush()
	}))

	req := httptest.NewRequest(http.MethodGet, "/metrics/stream", nil)
	rec := &flushRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onFlush: func() {
			flushed = true
		},
	}

	handler.ServeHTTP(rec, req)

	if !flushed {
		t.Fatal("Flush was not forwarded to the underlying writer")
	}
}

func TestRequestLoggerPreservesHijackerWhenAvailable(t *testing.T) {
	t.Parallel()

	handler := RequestLogger(zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Hijacker); !ok {
			t.Fatal("response writer does not implement http.Hijacker")
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/exec/jobs/1/stream", nil)
	handler.ServeHTTP(&hijackRecorder{ResponseRecorder: httptest.NewRecorder()}, req)
}

func TestRequestLoggerPreservesPusherWhenAvailable(t *testing.T) {
	t.Parallel()

	pushed := false
	handler := RequestLogger(zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pusher, ok := w.(http.Pusher)
		if !ok {
			t.Fatal("response writer does not implement http.Pusher")
		}
		if err := pusher.Push("/asset.js", nil); err != nil {
			t.Fatalf("Push returned error: %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := &pushRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onPush: func(target string) {
			if target == "/asset.js" {
				pushed = true
			}
		},
	}

	handler.ServeHTTP(rec, req)

	if !pushed {
		t.Fatal("Push was not forwarded to the underlying writer")
	}
}

func TestRequestLoggerPreservesReaderFromWhenAvailable(t *testing.T) {
	t.Parallel()

	var copied bytes.Buffer
	handler := RequestLogger(zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readerFrom, ok := w.(io.ReaderFrom)
		if !ok {
			t.Fatal("response writer does not implement io.ReaderFrom")
		}
		if _, err := readerFrom.ReadFrom(bytes.NewBufferString("stream-body")); err != nil {
			t.Fatalf("ReadFrom returned error: %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/files/stream", nil)
	rec := &readerFromRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		dst:              &copied,
	}

	handler.ServeHTTP(rec, req)

	if got := copied.String(); got != "stream-body" {
		t.Fatalf("copied body = %q, want %q", got, "stream-body")
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (r *flushRecorder) Flush() {
	if r.onFlush != nil {
		r.onFlush()
	}
	r.ResponseRecorder.Flush()
}

type hijackRecorder struct {
	*httptest.ResponseRecorder
}

func (r *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

type pushRecorder struct {
	*httptest.ResponseRecorder
	onPush func(target string)
}

func (r *pushRecorder) Push(target string, _ *http.PushOptions) error {
	if r.onPush != nil {
		r.onPush(target)
	}
	return nil
}

type readerFromRecorder struct {
	*httptest.ResponseRecorder
	dst *bytes.Buffer
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.dst.ReadFrom(src)
}
