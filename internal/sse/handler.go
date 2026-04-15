package sse

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type StreamFunc func(ctx context.Context, sw *Writer) error

func Handler(heartbeatInterval time.Duration, fn StreamFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw, err := NewWriter(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()

		errc := make(chan error, 1)
		go func() {
			errc <- fn(ctx, sw)
		}()

		for {
			select {
			case <-ticker.C:
				if err := sw.Heartbeat(); err != nil {
					return
				}
			case err := <-errc:
				if err != nil && !errors.Is(err, ErrClientGone) && !errors.Is(err, context.Canceled) {
					sw.Error(err.Error())
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}
}
