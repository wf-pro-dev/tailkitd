package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	dockertypescontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/wf-pro-dev/tailkit"
	"github.com/wf-pro-dev/tailkit/types"
	"github.com/wf-pro-dev/tailkitd/internal/helpers"
	"github.com/wf-pro-dev/tailkitd/internal/sse"
)

func (h *Handler) handleContainerStats(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		helpers.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "use GET")
		return
	}
	if !h.cfg.Containers.Permits("stats") {
		helpers.WriteError(w, http.StatusForbidden, "containers.stats not enabled in docker.toml", "")
		return
	}

	sse.Handler(h.streamHeartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.streamContainerStats(ctx, id, func(snapshot dockertypescontainer.StatsResponse) error {
			return sw.Send(tailkit.EventStatsSnapshot, snapshot)
		})
	})(w, r)
}

func (h *Handler) streamContainerLogs(w http.ResponseWriter, r *http.Request, id, tail string, timestamps bool) {
	sse.Handler(h.streamHeartbeatInterval, func(ctx context.Context, sw *sse.Writer) error {
		return h.followContainerLogs(ctx, id, tail, timestamps, func(line types.LogLine) error {
			return sw.Send(tailkit.EventLogLine, line)
		})
	})(w, r)
}

func (h *Handler) defaultStreamContainerStats(ctx context.Context, id string, fn func(dockertypescontainer.StatsResponse) error) error {
	resp, err := h.client.Docker().ContainerStats(ctx, id, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		var snapshot dockertypescontainer.StatsResponse
		if err := dec.Decode(&snapshot); err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := fn(snapshot); err != nil {
			return err
		}
	}
}

func (h *Handler) defaultFollowContainerLogs(ctx context.Context, id, tail string, timestamps bool, fn func(types.LogLine) error) error {
	rc, err := h.client.Docker().ContainerLogs(ctx, id, dockertypescontainer.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: timestamps,
		Tail:       tail,
	})
	if err != nil {
		return err
	}
	defer rc.Close()

	stdoutWriter := &logEmitterWriter{
		containerID: id,
		stream:      "stdout",
		timestamps:  timestamps,
		emit:        fn,
	}
	stderrWriter := &logEmitterWriter{
		containerID: id,
		stream:      "stderr",
		timestamps:  timestamps,
		emit:        fn,
	}

	if _, err := stdcopy.StdCopy(stdoutWriter, stderrWriter, rc); err != nil {
		return err
	}
	if err := stdoutWriter.Flush(); err != nil {
		return err
	}
	if err := stderrWriter.Flush(); err != nil {
		return err
	}
	return nil
}

type logEmitterWriter struct {
	containerID string
	stream      string
	timestamps  bool
	emit        func(types.LogLine) error
	buf         strings.Builder
}

func (w *logEmitterWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		s := w.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx == -1 {
			break
		}
		line := s[:idx]
		rest := s[idx+1:]
		w.buf.Reset()
		w.buf.WriteString(rest)
		if err := w.emitLine(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *logEmitterWriter) Flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	line := w.buf.String()
	w.buf.Reset()
	return w.emitLine(line)
}

func (w *logEmitterWriter) emitLine(line string) error {
	ts, msg := parseTimestampedLine(line, w.timestamps)
	return w.emit(types.LogLine{
		ContainerID: w.containerID,
		Stream:      w.stream,
		TS:          ts,
		Line:        msg,
	})
}

func parseTimestampedLine(line string, timestamps bool) (time.Time, string) {
	if !timestamps {
		return time.Time{}, line
	}
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return time.Time{}, line
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return time.Time{}, line
	}
	return ts, strings.TrimPrefix(line[i+1:], " ")
}

func streamRawLogLines(r io.Reader, containerID, stream string, timestamps bool, fn func(types.LogLine) error) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		ts, line := parseTimestampedLine(scanner.Text(), timestamps)
		if err := fn(types.LogLine{
			ContainerID: containerID,
			Stream:      stream,
			TS:          ts,
			Line:        line,
		}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan log stream: %w", err)
	}
	return nil
}
