package kernel

import (
	"fmt"
	"strings"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

type MembershipRole string

const (
	RoleOwner     MembershipRole = "owner"
	RoleAdmin     MembershipRole = "admin"
	RoleDeveloper MembershipRole = "developer"
	RoleViewer    MembershipRole = "viewer"
)

func (r MembershipRole) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer:
		return true
	default:
		return false
	}
}

type MembershipState string

const (
	MembershipInvited   MembershipState = "INVITED"
	MembershipActive    MembershipState = "ACTIVE"
	MembershipSuspended MembershipState = "SUSPENDED"
	MembershipRemoved   MembershipState = "REMOVED"
)

func (s MembershipState) Valid() bool {
	switch s {
	case MembershipInvited, MembershipActive, MembershipSuspended, MembershipRemoved:
		return true
	default:
		return false
	}
}

type Membership struct {
	PrincipalID kernelv1.PrincipalID
	Role        MembershipRole
	State       MembershipState
	InvitedAt   time.Time
	AcceptedAt  *time.Time
	UpdatedAt   time.Time
	Version     int64
}

func NewOwnerMembership(principalID kernelv1.PrincipalID, now time.Time) (*Membership, error) {
	if strings.TrimSpace(string(principalID)) == "" {
		return nil, invalidArgument("creator principal is required")
	}
	accepted := now.UTC()
	return &Membership{
		PrincipalID: principalID,
		Role:        RoleOwner,
		State:       MembershipActive,
		InvitedAt:   accepted,
		AcceptedAt:  &accepted,
		UpdatedAt:   accepted,
		Version:     1,
	}, nil
}

func NewInvitation(principalID kernelv1.PrincipalID, role MembershipRole, now time.Time) (*Membership, error) {
	if strings.TrimSpace(string(principalID)) == "" {
		return nil, invalidArgument("invited principal is required")
	}
	if !role.Valid() {
		return nil, invalidArgument("invalid membership role %q", role)
	}
	return &Membership{
		PrincipalID: principalID,
		Role:        role,
		State:       MembershipInvited,
		InvitedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
		Version:     1,
	}, nil
}

func (m *Membership) Clone() *Membership {
	if m == nil {
		return nil
	}
	copy := *m
	if m.AcceptedAt != nil {
		accepted := *m.AcceptedAt
		copy.AcceptedAt = &accepted
	}
	return &copy
}

func (m *Membership) Accept(now time.Time) error {
	if m.State != MembershipInvited {
		return conflict(fmt.Sprintf("membership cannot be accepted from %s", m.State))
	}
	accepted := now.UTC()
	m.State = MembershipActive
	m.AcceptedAt = &accepted
	m.UpdatedAt = accepted
	m.Version++
	return nil
}

func (m *Membership) ChangeRole(role MembershipRole, now time.Time) error {
	if !role.Valid() {
		return invalidArgument("invalid membership role %q", role)
	}
	if m.State != MembershipActive {
		return conflict("only active membership role can be changed")
	}
	if m.Role == role {
		return nil
	}
	m.Role = role
	m.UpdatedAt = now.UTC()
	m.Version++
	return nil
}

func (m *Membership) Suspend(now time.Time) error {
	if m.State == MembershipSuspended {
		return nil
	}
	if m.State != MembershipActive {
		return conflict(fmt.Sprintf("membership cannot be suspended from %s", m.State))
	}
	m.State = MembershipSuspended
	m.UpdatedAt = now.UTC()
	m.Version++
	return nil
}

func (m *Membership) Remove(now time.Time) error {
	if m.State == MembershipRemoved {
		return nil
	}
	switch m.State {
	case MembershipInvited, MembershipActive, MembershipSuspended:
		m.State = MembershipRemoved
		m.UpdatedAt = now.UTC()
		m.Version++
		return nil
	default:
		return conflict(fmt.Sprintf("membership cannot be removed from %s", m.State))
	}
}
