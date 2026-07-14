package session

import (
	"context"
	"sort"
	"sync"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	messages map[string]Message
	keys     map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]Session), messages: make(map[string]Message), keys: make(map[string]string)}
}

func (s *MemoryStore) Connect(_ context.Context, candidate Session) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, existing := range s.sessions {
		if existing.CertificateID == candidate.CertificateID {
			if existing.TenantID != candidate.TenantID || existing.ProjectID != candidate.ProjectID || existing.WorkspaceID != candidate.WorkspaceID || existing.TaskID != candidate.TaskID || existing.AgentID != candidate.AgentID || existing.VMID != candidate.VMID || existing.ClosedAt != nil {
				return Session{}, ErrConflict
			}
			existing.LastSeenAt = candidate.LastSeenAt
			existing.ExpiresAt = candidate.ExpiresAt
			existing.Version++
			s.sessions[id] = existing
			return cloneSession(existing), nil
		}
	}
	for id, existing := range s.sessions {
		if existing.WorkspaceID == candidate.WorkspaceID && existing.ClosedAt == nil {
			closed := candidate.ConnectedAt
			existing.ClosedAt = &closed
			existing.CloseReason = "certificate_rotated"
			existing.Version++
			s.sessions[id] = existing
		}
	}
	s.sessions[candidate.ID] = cloneSession(candidate)
	return cloneSession(candidate), nil
}

func (s *MemoryStore) GetSession(_ context.Context, sessionID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[sessionID]
	if !ok {
		return Session{}, ErrNotFound
	}
	return cloneSession(value), nil
}

func (s *MemoryStore) ActiveForWorkspace(_ context.Context, workspaceID, vmID string, now time.Time, idleTTL time.Duration) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Session
	for _, candidate := range s.sessions {
		if candidate.WorkspaceID == workspaceID && (vmID == "" || candidate.VMID == vmID) && candidate.Active(now, idleTTL) {
			result = append(result, candidate)
		}
	}
	if len(result) == 0 {
		return Session{}, ErrNotFound
	}
	if len(result) != 1 {
		return Session{}, ErrConflict
	}
	return cloneSession(result[0]), nil
}

func (s *MemoryStore) Touch(_ context.Context, sessionID, certificateID string, now time.Time) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[sessionID]
	if !ok || value.CertificateID != certificateID || value.ClosedAt != nil || !value.ExpiresAt.After(now) {
		return Session{}, ErrNotFound
	}
	value.LastSeenAt = now
	value.Version++
	s.sessions[sessionID] = value
	return cloneSession(value), nil
}

func (s *MemoryStore) CloseWorkspace(_ context.Context, workspaceID string, now time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.sessions {
		if value.WorkspaceID == workspaceID && value.ClosedAt == nil {
			closed := now
			value.ClosedAt = &closed
			value.CloseReason = reason
			value.Version++
			s.sessions[id] = value
		}
	}
	return nil
}

func (s *MemoryStore) Queue(_ context.Context, candidate Message) (Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := messageKey(candidate.WorkspaceID, candidate.CommandID, candidate.Kind)
	if existingID, ok := s.keys[key]; ok {
		existing := s.messages[existingID]
		if existing.PayloadHash != candidate.PayloadHash {
			return Message{}, false, ErrConflict
		}
		return cloneMessage(existing), false, nil
	}
	s.keys[key] = candidate.ID
	s.messages[candidate.ID] = cloneMessage(candidate)
	return cloneMessage(candidate), true, nil
}

func (s *MemoryStore) GetMessage(_ context.Context, messageID string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.messages[messageID]
	if !ok {
		return Message{}, ErrNotFound
	}
	return cloneMessage(value), nil
}

func (s *MemoryStore) ClaimNext(_ context.Context, sessionID, workspaceID string, now, leaseUntil time.Time) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionValue, ok := s.sessions[sessionID]
	if !ok || sessionValue.WorkspaceID != workspaceID || sessionValue.ClosedAt != nil || !sessionValue.ExpiresAt.After(now) {
		return Message{}, ErrNotFound
	}
	candidates := make([]Message, 0)
	for _, message := range s.messages {
		redeliverable := message.State == MessageDelivered && message.DeliveryLeaseUntil != nil && !message.DeliveryLeaseUntil.After(now)
		if message.WorkspaceID == workspaceID && (message.State == MessageQueued || redeliverable) {
			candidates = append(candidates, message)
		}
	}
	if len(candidates) == 0 {
		return Message{}, ErrNotFound
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	message := candidates[0]
	delivered := now
	lease := leaseUntil
	message.State = MessageDelivered
	message.SessionID = sessionID
	message.VMID = sessionValue.VMID
	message.DeliveryAttempts++
	message.DeliveredAt = &delivered
	message.DeliveryLeaseUntil = &lease
	s.messages[message.ID] = message
	return cloneMessage(message), nil
}

func (s *MemoryStore) Acknowledge(_ context.Context, sessionID, workspaceID, messageID string, accepted bool, now time.Time) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	message, ok := s.messages[messageID]
	if !ok || message.WorkspaceID != workspaceID {
		return Message{}, ErrNotFound
	}
	wanted := MessageRejected
	if accepted {
		wanted = MessageAcked
	}
	if message.State == wanted && message.SessionID == sessionID {
		return cloneMessage(message), nil
	}
	if message.State != MessageDelivered || message.SessionID != sessionID {
		return Message{}, ErrConflict
	}
	acknowledged := now
	message.State = wanted
	message.AcknowledgedAt = &acknowledged
	message.DeliveryLeaseUntil = nil
	s.messages[message.ID] = message
	return cloneMessage(message), nil
}

func messageKey(workspaceID, commandID string, kind workspacev1.AgentMessageKind) string {
	return workspaceID + "\x00" + commandID + "\x00" + string(kind)
}

func cloneSession(value Session) Session {
	if value.ClosedAt != nil {
		copy := *value.ClosedAt
		value.ClosedAt = &copy
	}
	return value
}

func cloneMessage(value Message) Message {
	value.Payload = value.View()
	if value.DeliveryLeaseUntil != nil {
		copy := *value.DeliveryLeaseUntil
		value.DeliveryLeaseUntil = &copy
	}
	if value.DeliveredAt != nil {
		copy := *value.DeliveredAt
		value.DeliveredAt = &copy
	}
	if value.AcknowledgedAt != nil {
		copy := *value.AcknowledgedAt
		value.AcknowledgedAt = &copy
	}
	return value
}
