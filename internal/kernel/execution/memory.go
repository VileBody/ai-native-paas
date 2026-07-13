package execution

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu         sync.RWMutex
	graphs     map[string]*Graph
	byIdentity map[string]string
	audit      []AuditRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{graphs: make(map[string]*Graph), byIdentity: make(map[string]string)}
}

func (s *MemoryStore) ClaimGraph(_ context.Context, candidate *Graph) (*Graph, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity := candidate.TenantID + "\x00" + candidate.ProjectID + "\x00" + candidate.IdempotencyKey
	if existingID, exists := s.byIdentity[identity]; exists {
		return s.graphs[existingID].Clone(), false, nil
	}
	s.graphs[candidate.GraphID] = candidate.Clone()
	s.byIdentity[identity] = candidate.GraphID
	return candidate.Clone(), true, nil
}

func (s *MemoryStore) GetGraph(_ context.Context, graphID string) (*Graph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	graph, exists := s.graphs[graphID]
	if !exists {
		return nil, fail(CodeNotFound, "operation graph not found")
	}
	return graph.Clone(), nil
}

func (s *MemoryStore) UpdateGraph(_ context.Context, graphID string, expectedVersion int64, mutate func(*Graph) error) (*Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	graph, exists := s.graphs[graphID]
	if !exists {
		return nil, fail(CodeNotFound, "operation graph not found")
	}
	if graph.Version != expectedVersion {
		return nil, fail(CodeConflict, "operation graph version is stale")
	}
	candidate := graph.Clone()
	if err := mutate(candidate); err != nil {
		return nil, err
	}
	s.graphs[graphID] = candidate
	return candidate.Clone(), nil
}

func (s *MemoryStore) AppendAudit(_ context.Context, record AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, record)
	return nil
}

func (s *MemoryStore) Audit() []AuditRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]AuditRecord(nil), s.audit...)
}

func (s *MemoryStore) GraphCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.graphs)
}
