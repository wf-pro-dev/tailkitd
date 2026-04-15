package sse

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

var ErrClientGone = errors.New("sse: client disconnected")

// Event is the shared SSE event envelope.
type Event struct {
	Name string
	ID   int64
	Data any
}

// Writer emits server-sent events on a single HTTP response stream.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
	seq     atomic.Int64
	done    <-chan struct{}
	mu      sync.Mutex
}

func NewWriter(w http.ResponseWriter, r *http.Request) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &Writer{
		w:       w,
		flusher: flusher,
		done:    r.Context().Done(),
	}, nil
}

func (sw *Writer) Send(name string, data any) error {
	select {
	case <-sw.done:
		return ErrClientGone
	default:
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	select {
	case <-sw.done:
		return ErrClientGone
	default:
	}

	id := sw.seq.Add(1)
	if _, err := fmt.Fprintf(sw.w, "event: %s\nid: %d\ndata: %s\n\n", name, id, raw); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}

func (sw *Writer) Heartbeat() error {
	select {
	case <-sw.done:
		return ErrClientGone
	default:
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	select {
	case <-sw.done:
		return ErrClientGone
	default:
	}

	if _, err := fmt.Fprint(sw.w, ": heartbeat\n\n"); err != nil {
		return err
	}
	sw.flusher.Flush()
	return nil
}

func (sw *Writer) Error(msg string) {
	_ = sw.Send("error", map[string]string{"error": msg})
}

func (sw *Writer) SetSequence(seq int64) {
	sw.seq.Store(seq)
}

func ResumeFrom(r *http.Request) int64 {
	value := r.Header.Get("Last-Event-ID")
	if value == "" {
		return 0
	}

	seq, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return seq
}
