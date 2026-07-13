package domain

import (
	"strings"
	"time"
	"unicode"
)

type Project struct {
	ID        string
	TenantID  string
	Name      string
	Slug      string
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewProject(id, tenantID, name string, now time.Time) (Project, error) {
	id, tenantID, name = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(name)
	if id == "" || tenantID == "" || name == "" {
		return Project{}, NewError(CodeInvalidArgument, "project id, tenant id and name are required")
	}
	slug := Slugify(name)
	if slug == "" {
		return Project{}, NewError(CodeInvalidArgument, "project name does not produce a valid slug")
	}
	now = now.UTC()
	return Project{ID: id, TenantID: tenantID, Name: name, Slug: slug, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (p *Project) Rename(name string, now time.Time) error {
	name = strings.TrimSpace(name)
	slug := Slugify(name)
	if name == "" || slug == "" {
		return NewError(CodeInvalidArgument, "invalid project name")
	}
	if p.Name == name {
		return nil
	}
	p.Name, p.Slug, p.UpdatedAt = name, slug, now.UTC()
	p.Version++
	return nil
}

func Slugify(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	dash := false
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if r > unicode.MaxASCII {
				continue
			}
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
