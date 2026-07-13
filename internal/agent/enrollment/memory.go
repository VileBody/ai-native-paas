package enrollment

import (
	"context"
	"errors"
	"sync"
	"time"
)

type MemoryStore struct {
	mu          sync.Mutex
	enrollments map[string]EnrollmentRecord
	refresh     map[string]RefreshRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{enrollments: make(map[string]EnrollmentRecord), refresh: make(map[string]RefreshRecord)}
}

func (s *MemoryStore) InsertEnrollment(_ context.Context, record EnrollmentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.enrollments[record.TokenHash]; exists {
		return errors.New("enrollment token already exists")
	}
	s.enrollments[record.TokenHash] = record
	return nil
}

func (s *MemoryStore) ConsumeEnrollment(_ context.Context, tokenHash, agentID string, now time.Time) (EnrollmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.enrollments[tokenHash]
	if !exists || record.Binding.AgentID != agentID || !record.ExpiresAt.After(now) || record.ConsumedAt != nil {
		return EnrollmentRecord{}, errors.New("enrollment token is invalid, expired, consumed, or bound to another agent")
	}
	consumed := now.UTC()
	record.ConsumedAt = &consumed
	s.enrollments[tokenHash] = record
	return record, nil
}

func (s *MemoryStore) InsertRefresh(_ context.Context, record RefreshRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.refresh[record.TokenHash]; exists {
		return errors.New("refresh credential already exists")
	}
	s.refresh[record.TokenHash] = record
	return nil
}

func (s *MemoryStore) GetRefresh(_ context.Context, tokenHash string) (RefreshRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.refresh[tokenHash]
	if !exists {
		return RefreshRecord{}, errors.New("refresh credential not found")
	}
	return record, nil
}

func (s *MemoryStore) RevokeRefresh(_ context.Context, tokenHash string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.refresh[tokenHash]
	if !exists {
		return errors.New("refresh credential not found")
	}
	if record.RevokedAt == nil {
		revoked := now.UTC()
		record.RevokedAt = &revoked
		s.refresh[tokenHash] = record
	}
	return nil
}
