package exec

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/wf-pro-dev/tailkit/types"
)

const jobTTL = 5 * time.Minute
const evictionInterval = 30 * time.Second

// jobRecord is an internal record pairing a JobResult with the time it was stored.
type jobRecord struct {
	result   types.JobResult
	storedAt time.Time
}

// JobStore is an in-memory store for async job results. Jobs are evicted
// automatically after jobTTL (5 minutes). Concurrent access is safe.
type JobStore struct {
	mu     sync.RWMutex
	jobs   map[string]jobRecord
	logger *zap.Logger
}

// NewJobStore constructs a JobStore and starts the background eviction goroutine.
// The goroutine exits when ctx is done — pass the same context as the server lifetime.
func NewJobStore(logger *zap.Logger) *JobStore {
	s := &JobStore{
		jobs:   make(map[string]jobRecord),
		logger: logger.With(zap.String("component", "exec.jobs")),
	}
	return s
}

// StartEviction begins the background eviction loop. Call once after construction.
// The loop exits when ctx is cancelled.
func (s *JobStore) StartEviction(ctx interface{ Done() <-chan struct{} }) {
	go s.evictLoop(ctx)
}

// NewJob creates a new job entry with status "accepted" and returns its ID.
// The returned ID should be given to the caller immediately before the job runs.
func (s *JobStore) NewJob() string {
	id := uuid.New().String()
	s.mu.Lock()
	s.jobs[id] = jobRecord{
		result: types.JobResult{
			JobID:  id,
			Status: types.JobStatusAccepted,
		},
		storedAt: time.Now(),
	}
	s.mu.Unlock()
	return id
}

// StoreResult replaces the job entry for id with the completed result.
// If id does not exist (evicted between acceptance and completion), the
// result is stored anyway — a late store is better than losing it.
func (s *JobStore) StoreResult(id string, result types.JobResult) {
	result.JobID = id // ensure ID is always set on the result
	s.mu.Lock()
	s.jobs[id] = jobRecord{
		result:   result,
		storedAt: time.Now(),
	}
	s.mu.Unlock()
}

// GetResult retrieves a job result by ID.
// Returns (result, true) if the job exists (accepted, running, or completed).
// Returns (zero, false) if the job has been evicted or never existed.
func (s *JobStore) GetResult(id string) (types.JobResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.jobs[id]
	if !ok {
		return types.JobResult{}, false
	}
	return rec.result, true
}

// ─── Eviction ─────────────────────────────────────────────────────────────────

type doneChan interface {
	Done() <-chan struct{}
}

func (s *JobStore) evictLoop(ctx doneChan) {
	ticker := time.NewTicker(evictionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.evict()
		}
	}
}

func (s *JobStore) evict() {
	cutoff := time.Now().Add(-jobTTL)
	s.mu.Lock()
	evicted := 0
	for id, rec := range s.jobs {
		if rec.storedAt.Before(cutoff) {
			delete(s.jobs, id)
			evicted++
		}
	}
	s.mu.Unlock()
	if evicted > 0 {
		s.logger.Info("evicted expired jobs", zap.Int("count", evicted))
	}
}
