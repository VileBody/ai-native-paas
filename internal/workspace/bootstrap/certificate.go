// Package bootstrap defines the short-lived identity material used only to
// start a disposable workspace agent.
package bootstrap

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

type Identity struct {
	TenantID    string
	ProjectID   string
	WorkspaceID string
	TaskID      string
	AgentID     string
}

func (i Identity) Validate() error {
	for _, value := range []string{i.TenantID, i.ProjectID, i.WorkspaceID, i.TaskID, i.AgentID} {
		if strings.TrimSpace(value) == "" || len(value) > 256 || strings.ContainsRune(value, '\x00') {
			return errors.New("workspace bootstrap identity is invalid")
		}
	}
	return nil
}

func (i Identity) SPIFFEURI(trustDomain string) (*url.URL, error) {
	if err := i.Validate(); err != nil || strings.TrimSpace(trustDomain) == "" || strings.ContainsAny(trustDomain, "/:@") {
		return nil, errors.New("workspace SPIFFE identity is invalid")
	}
	segments := []string{"tenant", i.TenantID, "project", i.ProjectID, "workspace", i.WorkspaceID, "task", i.TaskID, "agent", i.AgentID}
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return url.Parse("spiffe://" + trustDomain + "/" + strings.Join(segments, "/"))
}

type CertificateBundle struct {
	Certificate []byte
	PrivateKey  []byte
	CAChain     []byte
	IdentityURI string
	Serial      string
	NotAfter    time.Time
}

func (b *CertificateBundle) Clear() {
	if b == nil {
		return
	}
	for _, value := range [][]byte{b.Certificate, b.PrivateKey, b.CAChain} {
		for index := range value {
			value[index] = 0
		}
	}
	b.Certificate, b.PrivateKey, b.CAChain = nil, nil, nil
}

type CertificateIssuer interface {
	Issue(context.Context, Identity) (CertificateBundle, error)
}
