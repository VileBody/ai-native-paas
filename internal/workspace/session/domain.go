// Package session implements the outbound, mTLS-bound workspace agent channel.
package session

import (
	"errors"
	"regexp"
	"strings"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

var (
	ErrNotFound = errors.New("workspace agent session resource not found")
	ErrConflict = errors.New("workspace agent session resource conflicts with existing identity")
	identity    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type Principal struct {
	TenantID      string
	ProjectID     string
	WorkspaceID   string
	TaskID        string
	AgentID       string
	CertificateID string
	NotAfter      time.Time
}

func (p Principal) Validate(now time.Time, maximumLifetime time.Duration) error {
	for _, value := range []string{p.TenantID, p.ProjectID, p.WorkspaceID, p.TaskID, p.AgentID, p.CertificateID} {
		if !identity.MatchString(value) {
			return errors.New("workspace certificate identity is invalid")
		}
	}
	if !p.NotAfter.After(now) || maximumLifetime <= 0 || p.NotAfter.After(now.Add(maximumLifetime+time.Second)) {
		return errors.New("workspace certificate lifetime is invalid")
	}
	return nil
}

type Session struct {
	ID            string
	TenantID      string
	ProjectID     string
	WorkspaceID   string
	TaskID        string
	AgentID       string
	VMID          string
	CertificateID string
	ConnectedAt   time.Time
	LastSeenAt    time.Time
	ExpiresAt     time.Time
	ClosedAt      *time.Time
	CloseReason   string
	Version       int64
}

func (s Session) Active(now time.Time, idleTTL time.Duration) bool {
	return s.ClosedAt == nil && s.ExpiresAt.After(now) && s.LastSeenAt.Add(idleTTL).After(now)
}

type MessageState string

const (
	MessageQueued    MessageState = "QUEUED"
	MessageDelivered MessageState = "DELIVERED"
	MessageAcked     MessageState = "ACKED"
	MessageRejected  MessageState = "REJECTED"
)

type Message struct {
	ID                 string
	WorkspaceID        string
	CommandID          string
	Kind               workspacev1.AgentMessageKind
	Payload            workspacev1.AgentMessage
	PayloadHash        string
	State              MessageState
	SessionID          string
	VMID               string
	DeliveryAttempts   int64
	DeliveryLeaseUntil *time.Time
	CreatedAt          time.Time
	DeliveredAt        *time.Time
	AcknowledgedAt     *time.Time
}

func (m Message) View() workspacev1.AgentMessage {
	result := m.Payload
	result.MessageID = m.ID
	result.Kind = m.Kind
	result.CommandID = m.CommandID
	result.WorkspaceID = m.WorkspaceID
	result.DeliveryAttempt = m.DeliveryAttempts
	if result.Spec != nil {
		copy := *result.Spec
		copy.Argv = append([]string(nil), copy.Argv...)
		copy.EnvironmentRefs = cloneMap(copy.EnvironmentRefs)
		result.Spec = &copy
	}
	result.CredentialLeases = append([]string(nil), result.CredentialLeases...)
	return result
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

func validVMID(value string) bool {
	return identity.MatchString(strings.TrimSpace(value))
}
