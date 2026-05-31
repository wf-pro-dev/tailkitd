package invite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wf-pro-dev/tailkitd/internal/utils"
	"go.uber.org/zap"
)

var ClaimsStorePath = "/var/lib/tailkitd/invites/claims.json"

type ClaimRecord struct {
	TokenID   string    `json:"token_id"`
	ClaimedBy string    `json:"claimed_by"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Store struct {
	mu     sync.RWMutex
	path   string
	claims map[string]ClaimRecord
	logger *zap.Logger
}

func NewStore(path string, logger *zap.Logger) (*Store, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Store{
		path:   path,
		claims: map[string]ClaimRecord{},
		logger: logger.With(zap.String("component", "invite.store")),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Debug("invite claims store not found; starting empty", zap.String("path", s.path))
			return nil
		}
		return fmt.Errorf("invite store: read %s: %w", s.path, err)
	}
	var claims []ClaimRecord
	if err := json.Unmarshal(data, &claims); err != nil {
		return fmt.Errorf("invite store: parse %s: %w", s.path, err)
	}
	for _, claim := range claims {
		s.claims[claim.TokenID] = claim
	}
	s.logger.Debug("invite claims store loaded",
		zap.String("path", s.path),
		zap.Int("claim_count", len(s.claims)),
	)
	return nil
}

func (s *Store) IsClaimed(tokenID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.claims[tokenID]
	return ok
}

func (s *Store) MarkClaimed(tokenID, claimedBy string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.claims[tokenID]; ok {
		return fmt.Errorf("invite token already claimed")
	}
	s.claims[tokenID] = ClaimRecord{
		TokenID:   tokenID,
		ClaimedBy: claimedBy,
		ClaimedAt: time.Now().UTC(),
		ExpiresAt: expiresAt.UTC(),
	}
	if err := s.persistLocked(); err != nil {
		return err
	}
	s.logger.Info("invite claim recorded",
		zap.String("path", s.path),
		zap.String("token_id", tokenID),
		zap.String("claimed_by", claimedBy),
		zap.Time("expires_at", expiresAt.UTC()),
	)
	return nil
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	claims := make([]ClaimRecord, 0, len(s.claims))
	for _, claim := range s.claims {
		claims = append(claims, claim)
	}
	data, err := json.MarshalIndent(claims, "", "  ")
	if err != nil {
		return err
	}
	return utils.AtomicWrite(s.path, data, 0o600)
}
