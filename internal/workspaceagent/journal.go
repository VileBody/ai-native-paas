package workspaceagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type JournalState string

const (
	JournalAccepted JournalState = "ACCEPTED"
	JournalRunning  JournalState = "RUNNING"
	JournalTerminal JournalState = "TERMINAL"
)

type JournalRecord struct {
	Version            string                           `json:"version"`
	CommandID          string                           `json:"command_id"`
	WorkspaceID        string                           `json:"workspace_id"`
	MessageHash        string                           `json:"message_hash"`
	Spec               workspacev1.CommandSpec          `json:"spec"`
	ExecutionSessionID string                           `json:"execution_session_id,omitempty"`
	State              JournalState                     `json:"state"`
	Outcome            *workspacev1.AgentCommandOutcome `json:"outcome,omitempty"`
	OutputReady        bool                             `json:"output_ready"`
	OutputUploaded     bool                             `json:"output_uploaded"`
	Reported           bool                             `json:"reported"`
	UpdatedAt          time.Time                        `json:"updated_at"`
}

type Journal struct {
	directory   string
	workspaceID string
	now         func() time.Time
	mu          sync.Mutex
}

func NewJournal(directory, workspaceID string, now func() time.Time) (*Journal, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || !identityPattern.MatchString(workspaceID) || now == nil {
		return nil, errors.New("workspace command journal configuration is invalid")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("create workspace command journal")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, errors.New("secure workspace command journal")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("workspace command journal permissions are invalid")
	}
	return &Journal{directory: directory, workspaceID: workspaceID, now: now}, nil
}

func (j *Journal) Prepare(message workspacev1.AgentMessage) (JournalRecord, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if message.Kind != workspacev1.AgentMessageExec || !identityPattern.MatchString(message.CommandID) || message.WorkspaceID != j.workspaceID || message.Spec == nil || message.Spec.Validate() != nil || message.DeliveryAttempt < 1 || !validCommandBudget(message, j.now().UTC()) {
		return JournalRecord{}, false, errors.New("workspace command message is invalid")
	}
	hash, err := messageFingerprint(message)
	if err != nil {
		return JournalRecord{}, false, err
	}
	existing, err := j.load(message.CommandID)
	if err == nil {
		if existing.MessageHash != hash || existing.WorkspaceID != message.WorkspaceID || existing.CommandID != message.CommandID {
			return JournalRecord{}, false, errors.New("workspace command redelivery changed immutable payload")
		}
		return existing, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return JournalRecord{}, false, err
	}
	record := JournalRecord{
		Version: "workspace.platform.example.com/agent-journal/v1", CommandID: message.CommandID, WorkspaceID: message.WorkspaceID,
		MessageHash: hash, Spec: cloneCommandSpec(*message.Spec), State: JournalAccepted, UpdatedAt: j.now().UTC(),
	}
	if err := j.persist(record); err != nil {
		return JournalRecord{}, false, err
	}
	return record, true, nil
}

func (j *Journal) MarkRunning(commandID, executionSessionID string) (JournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.load(commandID)
	if err != nil {
		return JournalRecord{}, err
	}
	if !identityPattern.MatchString(executionSessionID) {
		return JournalRecord{}, errors.New("workspace execution session identity is invalid")
	}
	if record.State == JournalRunning {
		if record.ExecutionSessionID != executionSessionID {
			return JournalRecord{}, errors.New("workspace execution session identity changed")
		}
		return record, nil
	}
	if record.State != JournalAccepted {
		return JournalRecord{}, errors.New("workspace command cannot enter running state")
	}
	record.State = JournalRunning
	record.ExecutionSessionID = executionSessionID
	record.UpdatedAt = j.now().UTC()
	return record, j.persist(record)
}

func (j *Journal) Finish(commandID string, outcome workspacev1.AgentCommandOutcome) (JournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.load(commandID)
	if err != nil {
		return JournalRecord{}, err
	}
	if outcome.CommandID != commandID || !terminalOutcome(outcome.State) || outcome.FinishedAt.IsZero() {
		return JournalRecord{}, errors.New("workspace command outcome is invalid")
	}
	if record.State == JournalTerminal {
		if record.Outcome == nil || !sameOutcome(*record.Outcome, outcome) {
			return JournalRecord{}, errors.New("workspace command terminal outcome changed")
		}
		return record, nil
	}
	if record.State != JournalRunning && record.State != JournalAccepted {
		return JournalRecord{}, errors.New("workspace command journal state is invalid")
	}
	outcome.SessionID = ""
	record.State = JournalTerminal
	record.Outcome = &outcome
	record.Reported = false
	record.UpdatedAt = j.now().UTC()
	return record, j.persist(record)
}

func (j *Journal) MarkReported(commandID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.load(commandID)
	if err != nil {
		return err
	}
	if record.State != JournalTerminal || record.Outcome == nil || !record.OutputUploaded {
		return errors.New("workspace command has no terminal outcome")
	}
	if record.Reported {
		return nil
	}
	record.Reported = true
	record.UpdatedAt = j.now().UTC()
	return j.persist(record)
}

func (j *Journal) MarkOutputReady(commandID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.load(commandID)
	if err != nil {
		return err
	}
	if record.State != JournalRunning && record.State != JournalTerminal {
		return errors.New("workspace command cannot persist output")
	}
	if record.OutputReady {
		return nil
	}
	record.OutputReady = true
	record.UpdatedAt = j.now().UTC()
	return j.persist(record)
}

func (j *Journal) MarkOutputUploaded(commandID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.load(commandID)
	if err != nil {
		return err
	}
	if record.State != JournalTerminal || record.Outcome == nil || !record.OutputReady {
		return errors.New("workspace command output is not ready")
	}
	if record.OutputUploaded {
		return nil
	}
	record.OutputUploaded = true
	record.UpdatedAt = j.now().UTC()
	return j.persist(record)
}

func (j *Journal) PendingOutcomes() ([]JournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	records, err := j.list()
	if err != nil {
		return nil, err
	}
	result := make([]JournalRecord, 0)
	for _, record := range records {
		if record.State == JournalTerminal && record.Outcome != nil && !record.Reported {
			result = append(result, record)
		}
	}
	return result, nil
}

// RecoverInterrupted never replays a command whose process may already have
// produced external effects. The systemd unit owns the command cgroup and
// kills it on agent restart; recovery emits one durable failure outcome.
func (j *Journal) RecoverInterrupted() (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	records, err := j.list()
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, record := range records {
		if record.State != JournalRunning {
			continue
		}
		record.State = JournalTerminal
		record.Outcome = &workspacev1.AgentCommandOutcome{
			CommandID: record.CommandID, ExecutionSessionID: record.ExecutionSessionID, State: workspacev1.CommandFailed, FinishedAt: j.now().UTC(), ProcessTreeTerminated: true,
		}
		record.Reported = false
		record.UpdatedAt = j.now().UTC()
		if err := j.persist(record); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func (j *Journal) load(commandID string) (JournalRecord, error) {
	if !identityPattern.MatchString(commandID) {
		return JournalRecord{}, errors.New("workspace command identity is invalid")
	}
	file, err := os.Open(j.filename(commandID))
	if err != nil {
		return JournalRecord{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > 1<<20 {
		return JournalRecord{}, errors.New("workspace command journal record is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return JournalRecord{}, errors.New("workspace command journal record is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var record JournalRecord
	if err := decoder.Decode(&record); err != nil || record.Version != "workspace.platform.example.com/agent-journal/v1" || record.CommandID != commandID || record.WorkspaceID != j.workspaceID || record.Spec.Validate() != nil {
		return JournalRecord{}, errors.New("workspace command journal record is invalid")
	}
	return record, nil
}

func (j *Journal) list() ([]JournalRecord, error) {
	entries, err := os.ReadDir(j.directory)
	if err != nil {
		return nil, err
	}
	result := make([]JournalRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(j.directory, entry.Name()))
		if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
			return nil, errors.New("workspace command journal contains invalid record")
		}
		var record JournalRecord
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil || record.Version != "workspace.platform.example.com/agent-journal/v1" || record.WorkspaceID != j.workspaceID || !identityPattern.MatchString(record.CommandID) || record.Spec.Validate() != nil || filepath.Base(j.filename(record.CommandID)) != entry.Name() {
			return nil, errors.New("workspace command journal contains invalid record")
		}
		result = append(result, record)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].UpdatedAt.Before(result[right].UpdatedAt) })
	return result, nil
}

func (j *Journal) persist(record JournalRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeAtomic(j.filename(record.CommandID), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return syncDirectory(j.directory)
}

func (j *Journal) filename(commandID string) string {
	digest := sha256.Sum256([]byte(commandID))
	return filepath.Join(j.directory, hex.EncodeToString(digest[:])+".json")
}

func messageFingerprint(message workspacev1.AgentMessage) (string, error) {
	payload := struct {
		Kind             workspacev1.AgentMessageKind `json:"kind"`
		CommandID        string                       `json:"command_id"`
		WorkspaceID      string                       `json:"workspace_id"`
		Spec             *workspacev1.CommandSpec     `json:"spec"`
		CredentialLeases []string                     `json:"credential_leases,omitempty"`
	}{message.Kind, message.CommandID, message.WorkspaceID, message.Spec, message.CredentialLeases}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func cloneCommandSpec(value workspacev1.CommandSpec) workspacev1.CommandSpec {
	value.Argv = append([]string(nil), value.Argv...)
	if value.EnvironmentRefs != nil {
		copy := make(map[string]string, len(value.EnvironmentRefs))
		for name, reference := range value.EnvironmentRefs {
			copy[name] = reference
		}
		value.EnvironmentRefs = copy
	}
	return value
}

func terminalOutcome(state workspacev1.CommandState) bool {
	switch state {
	case workspacev1.CommandSucceeded, workspacev1.CommandFailed, workspacev1.CommandCanceled, workspacev1.CommandTimedOut:
		return true
	default:
		return false
	}
}

func sameOutcome(left, right workspacev1.AgentCommandOutcome) bool {
	return left.CommandID == right.CommandID && left.State == right.State && equalExitCode(left.ExitCode, right.ExitCode) && left.FinishedAt.Equal(right.FinishedAt) && left.ProcessTreeTerminated == right.ProcessTreeTerminated
}

func equalExitCode(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
