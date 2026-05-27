package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/wf-pro-dev/tailkitd/internal/utils"
)

var EpochFilePath = "/etc/tailkitd/state.epoch"

type Epoch struct {
	mu    sync.RWMutex
	path  string
	value int64
}

func NewEpoch(path string) (*Epoch, error) {
	e := &Epoch{path: path}
	if err := e.load(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Epoch) load() error {
	data, err := os.ReadFile(e.path)
	if err != nil {
		if os.IsNotExist(err) {
			e.value = 0
			return nil
		}
		return fmt.Errorf("state epoch: read %s: %w", e.path, err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return fmt.Errorf("state epoch: parse %s: %w", e.path, err)
	}
	e.value = n
	return nil
}

func (e *Epoch) Current() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.value
}

func (e *Epoch) Validate(caller int64) error {
	current := e.Current()
	switch {
	case caller < current:
		return fmt.Errorf("stale epoch: caller=%d current=%d", caller, current)
	case caller > current:
		return fmt.Errorf("future epoch: caller=%d current=%d", caller, current)
	default:
		return nil
	}
}

func (e *Epoch) Increment() (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.value++
	if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
		return 0, err
	}
	if err := utils.AtomicWrite(e.path, []byte(fmt.Sprintf("%d\n", e.value)), 0o600); err != nil {
		return 0, err
	}
	return e.value, nil
}
