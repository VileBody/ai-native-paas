package workspace

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu             sync.Mutex
	workspaces     map[string]Workspace
	workspaceIdem  map[string]string
	workspaceTasks map[string]string
	commands       map[string]Command
	commandIdem    map[string]string
	serializations map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		workspaces: make(map[string]Workspace), workspaceIdem: make(map[string]string), workspaceTasks: make(map[string]string),
		commands: make(map[string]Command), commandIdem: make(map[string]string), serializations: make(map[string]string),
	}
}

func (s *MemoryStore) CreateWorkspace(_ context.Context, candidate Workspace) (Workspace, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idemKey := scopedKey(candidate.TenantID, candidate.ProjectID, candidate.IdempotencyKey)
	if id, ok := s.workspaceIdem[idemKey]; ok {
		existing := s.workspaces[id]
		if existing.RequestHash != candidate.RequestHash {
			return Workspace{}, false, ErrConflict
		}
		return cloneWorkspace(existing), false, nil
	}
	taskKey := scopedKey(candidate.TenantID, candidate.ProjectID, candidate.TaskID)
	if id, ok := s.workspaceTasks[taskKey]; ok {
		existing := s.workspaces[id]
		if existing.RequestHash != candidate.RequestHash {
			return Workspace{}, false, ErrConflict
		}
		s.workspaceIdem[idemKey] = id
		return cloneWorkspace(existing), false, nil
	}
	if _, exists := s.workspaces[candidate.ID]; exists {
		return Workspace{}, false, ErrConflict
	}
	s.workspaces[candidate.ID] = cloneWorkspace(candidate)
	s.workspaceIdem[idemKey] = candidate.ID
	s.workspaceTasks[taskKey] = candidate.ID
	return cloneWorkspace(candidate), true, nil
}

func (s *MemoryStore) GetWorkspace(_ context.Context, tenantID, projectID, workspaceID string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace, ok := s.workspaces[workspaceID]
	if !ok || workspace.TenantID != tenantID || workspace.ProjectID != projectID {
		return Workspace{}, ErrNotFound
	}
	return cloneWorkspace(workspace), nil
}

func (s *MemoryStore) UpdateWorkspace(_ context.Context, workspace Workspace, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.workspaces[workspace.ID]
	if !ok {
		return ErrNotFound
	}
	if existing.Version != expected || workspace.Version != expected+1 || existing.TenantID != workspace.TenantID || existing.ProjectID != workspace.ProjectID {
		return ErrConflict
	}
	s.workspaces[workspace.ID] = cloneWorkspace(workspace)
	return nil
}

func (s *MemoryStore) ClaimWorkspace(_ context.Context, workspaceID, owner string, now, until time.Time) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace, ok := s.workspaces[workspaceID]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	if workspace.State != "PROVISIONING" && workspace.State != "DESTROYING" {
		return cloneWorkspace(workspace), nil
	}
	if workspace.ReconcileOwner != "" && workspace.ReconcileOwner != owner && workspace.ReconcileLeaseUntil.After(now) {
		return Workspace{}, ErrReconcileClaimed
	}
	workspace.ReconcileOwner = owner
	workspace.ReconcileLeaseUntil = until
	workspace.UpdatedAt = now
	workspace.Version++
	s.workspaces[workspaceID] = workspace
	return cloneWorkspace(workspace), nil
}

func (s *MemoryStore) ListExpired(_ context.Context, now time.Time, limit int) ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Workspace, 0)
	for _, workspace := range s.workspaces {
		if !workspace.ExpiresAt.After(now) && workspace.State != "DESTROYED" && workspace.State != "DESTROYING" {
			result = append(result, cloneWorkspace(workspace))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ExpiresAt.Equal(result[j].ExpiresAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].ExpiresAt.Before(result[j].ExpiresAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *MemoryStore) CreateCommand(_ context.Context, candidate Command) (Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idemKey := scopedKey(candidate.TenantID, candidate.ProjectID, candidate.IdempotencyKey)
	if id, ok := s.commandIdem[idemKey]; ok {
		existing := s.commands[id]
		if existing.RequestHash != candidate.RequestHash {
			return Command{}, false, ErrConflict
		}
		return cloneCommand(existing), false, nil
	}
	if _, exists := s.commands[candidate.ID]; exists {
		return Command{}, false, ErrConflict
	}
	s.commands[candidate.ID] = cloneCommand(candidate)
	s.commandIdem[idemKey] = candidate.ID
	return cloneCommand(candidate), true, nil
}

func (s *MemoryStore) GetCommand(_ context.Context, tenantID, projectID, commandID string) (Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.commands[commandID]
	if !ok || command.TenantID != tenantID || command.ProjectID != projectID {
		return Command{}, ErrNotFound
	}
	return cloneCommand(command), nil
}

func (s *MemoryStore) UpdateCommand(_ context.Context, command Command, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.commands[command.ID]
	if !ok {
		return ErrNotFound
	}
	if existing.Version != expected || command.Version != expected+1 || existing.WorkspaceID != command.WorkspaceID {
		return ErrConflict
	}
	s.commands[command.ID] = cloneCommand(command)
	return nil
}

func (s *MemoryStore) ListTimedOut(_ context.Context, now time.Time, limit int) ([]Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Command, 0)
	for _, command := range s.commands {
		if command.State == "RUNNING" && command.StartedAt != nil && !command.StartedAt.Add(time.Duration(command.Spec.TimeoutSeconds)*time.Second).After(now) {
			result = append(result, cloneCommand(command))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartedAt.Before(*result[j].StartedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *MemoryStore) AcquireSerialization(_ context.Context, projectID, key, commandID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lockKey := projectID + "\x00" + key
	owner, exists := s.serializations[lockKey]
	if exists && owner != commandID {
		return false, nil
	}
	s.serializations[lockKey] = commandID
	return true, nil
}

func (s *MemoryStore) ReleaseSerialization(_ context.Context, projectID, key, commandID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lockKey := projectID + "\x00" + key
	owner, exists := s.serializations[lockKey]
	if !exists {
		return nil
	}
	if owner != commandID {
		return ErrConflict
	}
	delete(s.serializations, lockKey)
	return nil
}

func scopedKey(values ...string) string {
	result := ""
	for _, value := range values {
		result += string(rune(len(value))) + value
	}
	return result
}

func cloneWorkspace(value Workspace) Workspace {
	value.Spec.CredentialLeases = append([]string(nil), value.Spec.CredentialLeases...)
	value.ProviderDiskIDs = append([]string(nil), value.ProviderDiskIDs...)
	return value
}

func cloneCommand(value Command) Command {
	value.Spec.Argv = append([]string(nil), value.Spec.Argv...)
	value.Spec.EnvironmentRefs = cloneMap(value.Spec.EnvironmentRefs)
	value.CredentialLeases = append([]string(nil), value.CredentialLeases...)
	value.ExitCode = copyInt(value.ExitCode)
	value.StartedAt = cloneTime(value.StartedAt)
	value.FinishedAt = cloneTime(value.FinishedAt)
	value.UsageStartedAt = cloneTime(value.UsageStartedAt)
	value.UsageFinishedAt = cloneTime(value.UsageFinishedAt)
	return value
}

func cloneMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
