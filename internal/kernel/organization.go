package kernel

import (
	"strings"
	"time"
	"unicode"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

type Organization struct {
	ID          kernelv1.TenantID
	Name        string
	Slug        string
	Memberships map[kernelv1.PrincipalID]*Membership
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func NewOrganization(id kernelv1.TenantID, name string, creator kernelv1.PrincipalID, now time.Time) (*Organization, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, invalidArgument("organization id is required")
	}
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" {
		return nil, invalidArgument("organization name is required")
	}
	slug := NormalizeSlug(normalizedName)
	if slug == "" {
		return nil, invalidArgument("organization name does not produce a valid slug")
	}
	owner, err := NewOwnerMembership(creator, now)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	return &Organization{
		ID:          id,
		Name:        normalizedName,
		Slug:        slug,
		Memberships: map[kernelv1.PrincipalID]*Membership{creator: owner},
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func NormalizeSlug(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	var b strings.Builder
	dash := false
	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (o *Organization) Clone() *Organization {
	if o == nil {
		return nil
	}
	copy := *o
	copy.Memberships = make(map[kernelv1.PrincipalID]*Membership, len(o.Memberships))
	for principalID, membership := range o.Memberships {
		copy.Memberships[principalID] = membership.Clone()
	}
	return &copy
}

func (o *Organization) Invite(principalID kernelv1.PrincipalID, role MembershipRole, now time.Time) error {
	existing, exists := o.Memberships[principalID]
	if exists && existing.State != MembershipRemoved {
		return conflict("principal already has a membership")
	}
	invitation, err := NewInvitation(principalID, role, now)
	if err != nil {
		return err
	}
	o.Memberships[principalID] = invitation
	o.touch(now)
	return nil
}

func (o *Organization) AcceptInvitation(principalID kernelv1.PrincipalID, now time.Time) error {
	membership, ok := o.Memberships[principalID]
	if !ok {
		return notFound("membership invitation")
	}
	if err := membership.Accept(now); err != nil {
		return err
	}
	o.touch(now)
	return nil
}

func (o *Organization) ChangeMemberRole(principalID kernelv1.PrincipalID, role MembershipRole, now time.Time) error {
	membership, ok := o.Memberships[principalID]
	if !ok {
		return notFound("membership")
	}
	if membership.Role == RoleOwner && role != RoleOwner && membership.State == MembershipActive && o.activeOwnerCount() <= 1 {
		return NewError(kernelv1.CodeLastOwner, "organization must retain at least one active owner")
	}
	before := membership.Version
	if err := membership.ChangeRole(role, now); err != nil {
		return err
	}
	if membership.Version != before {
		o.touch(now)
	}
	return nil
}

func (o *Organization) SuspendMember(principalID kernelv1.PrincipalID, now time.Time) error {
	membership, ok := o.Memberships[principalID]
	if !ok {
		return notFound("membership")
	}
	if membership.Role == RoleOwner && membership.State == MembershipActive && o.activeOwnerCount() <= 1 {
		return NewError(kernelv1.CodeLastOwner, "organization must retain at least one active owner")
	}
	before := membership.Version
	if err := membership.Suspend(now); err != nil {
		return err
	}
	if membership.Version != before {
		o.touch(now)
	}
	return nil
}

func (o *Organization) RemoveMember(principalID kernelv1.PrincipalID, now time.Time) error {
	membership, ok := o.Memberships[principalID]
	if !ok {
		return notFound("membership")
	}
	if membership.Role == RoleOwner && membership.State == MembershipActive && o.activeOwnerCount() <= 1 {
		return NewError(kernelv1.CodeLastOwner, "organization must retain at least one active owner")
	}
	before := membership.Version
	if err := membership.Remove(now); err != nil {
		return err
	}
	if membership.Version != before {
		o.touch(now)
	}
	return nil
}

func (o *Organization) Membership(principalID kernelv1.PrincipalID) (*Membership, bool) {
	membership, ok := o.Memberships[principalID]
	if !ok {
		return nil, false
	}
	return membership.Clone(), true
}

func (o *Organization) activeOwnerCount() int {
	count := 0
	for _, membership := range o.Memberships {
		if membership.State == MembershipActive && membership.Role == RoleOwner {
			count++
		}
	}
	return count
}

func (o *Organization) touch(now time.Time) {
	o.Version++
	o.UpdatedAt = now.UTC()
}
