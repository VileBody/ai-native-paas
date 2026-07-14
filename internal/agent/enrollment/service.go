// Package enrollment implements one-time agent enrollment and short-lived project access credentials.
package enrollment

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	EnrollmentTTL = 10 * time.Minute
	AccessTTL     = 15 * time.Minute
	RefreshTTL    = 24 * time.Hour
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ New(string) string }
type SecretGenerator interface{ Secret(int) (string, error) }

type Binding struct {
	TenantID  string   `json:"tenant_id"`
	ProjectID string   `json:"project_id"`
	UserID    string   `json:"user_id"`
	AgentID   string   `json:"agent_id"`
	Scopes    []string `json:"scopes"`
}

func (b Binding) Validate() error {
	if b.TenantID == "" || b.ProjectID == "" || b.UserID == "" || b.AgentID == "" || len(b.Scopes) == 0 {
		return errors.New("agent binding is incomplete")
	}
	for _, scope := range b.Scopes {
		if !strings.HasPrefix(scope, "agent.tool:") {
			return errors.New("agent binding contains invalid scope")
		}
	}
	return nil
}

type EnrollmentRecord struct {
	EnrollmentID string
	TokenHash    string
	Binding      Binding
	ExpiresAt    time.Time
	ConsumedAt   *time.Time
}

type RefreshRecord struct {
	CredentialID string
	TokenHash    string
	Binding      Binding
	PublicKey    string
	ExpiresAt    time.Time
	RevokedAt    *time.Time
}

type Store interface {
	InsertEnrollment(context.Context, EnrollmentRecord) error
	ConsumeEnrollment(context.Context, string, string, time.Time) (EnrollmentRecord, error)
	InsertRefresh(context.Context, RefreshRecord) error
	GetRefresh(context.Context, string) (RefreshRecord, error)
	RevokeRefresh(context.Context, string, time.Time) error
}

type AccessClaims struct {
	Issuer       string   `json:"iss"`
	Audience     string   `json:"aud"`
	Subject      string   `json:"sub"`
	TenantID     string   `json:"tenant_id"`
	ProjectID    string   `json:"project_id"`
	UserID       string   `json:"user_id"`
	AgentID      string   `json:"agent_id"`
	CredentialID string   `json:"credential_id"`
	Scopes       []string `json:"scopes"`
	IssuedAt     int64    `json:"iat"`
	ExpiresAt    int64    `json:"exp"`
	JWTID        string   `json:"jti"`
}

type Signer interface {
	Sign(AccessClaims) (string, error)
	Verify(string) (AccessClaims, error)
}

type WorkspaceCertificateSubject struct {
	TenantID    string
	ProjectID   string
	AgentID     string
	WorkspaceID string
	TaskID      string
}

type WorkspaceCertificate struct {
	CertificateID  string    `json:"certificate_id"`
	CertificatePEM string    `json:"certificate_pem"`
	CAChainPEM     string    `json:"ca_chain_pem"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type WorkspaceCertificateIssuer interface {
	IssueWorkspaceCertificate(context.Context, WorkspaceCertificateSubject, string, time.Duration) (WorkspaceCertificate, error)
}

type Service struct {
	Store                 Store
	Clock                 Clock
	IDs                   IDGenerator
	Secrets               SecretGenerator
	Signer                Signer
	WorkspaceCertificates WorkspaceCertificateIssuer
}

type EnrollmentToken struct {
	EnrollmentID string
	Token        string
	ExpiresAt    time.Time
}

type RefreshCredential struct {
	CredentialID string
	RefreshToken string
	ExpiresAt    time.Time
}

func (s *Service) Issue(ctx context.Context, binding Binding) (EnrollmentToken, error) {
	if err := s.require(); err != nil {
		return EnrollmentToken{}, err
	}
	if err := binding.Validate(); err != nil {
		return EnrollmentToken{}, err
	}
	token, err := s.Secrets.Secret(32)
	if err != nil {
		return EnrollmentToken{}, err
	}
	now := s.Clock.Now().UTC()
	record := EnrollmentRecord{EnrollmentID: s.IDs.New("enrollment"), TokenHash: tokenHash(token), Binding: binding, ExpiresAt: now.Add(EnrollmentTTL)}
	if err := s.Store.InsertEnrollment(ctx, record); err != nil {
		return EnrollmentToken{}, err
	}
	return EnrollmentToken{EnrollmentID: record.EnrollmentID, Token: token, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Service) Exchange(ctx context.Context, enrollmentToken, agentID, publicKey string) (RefreshCredential, error) {
	if err := s.require(); err != nil {
		return RefreshCredential{}, err
	}
	if strings.TrimSpace(enrollmentToken) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(publicKey) == "" {
		return RefreshCredential{}, errors.New("enrollment exchange is incomplete")
	}
	now := s.Clock.Now().UTC()
	enrollment, err := s.Store.ConsumeEnrollment(ctx, tokenHash(enrollmentToken), agentID, now)
	if err != nil {
		return RefreshCredential{}, err
	}
	refreshToken, err := s.Secrets.Secret(48)
	if err != nil {
		return RefreshCredential{}, err
	}
	record := RefreshRecord{CredentialID: s.IDs.New("credential"), TokenHash: tokenHash(refreshToken), Binding: enrollment.Binding, PublicKey: publicKey, ExpiresAt: now.Add(RefreshTTL)}
	if err := s.Store.InsertRefresh(ctx, record); err != nil {
		return RefreshCredential{}, err
	}
	return RefreshCredential{CredentialID: record.CredentialID, RefreshToken: refreshToken, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Service) Access(ctx context.Context, refreshToken string) (string, AccessClaims, error) {
	if err := s.require(); err != nil {
		return "", AccessClaims{}, err
	}
	record, err := s.Store.GetRefresh(ctx, tokenHash(refreshToken))
	if err != nil {
		return "", AccessClaims{}, err
	}
	now := s.Clock.Now().UTC()
	if record.RevokedAt != nil || !record.ExpiresAt.After(now) {
		return "", AccessClaims{}, errors.New("refresh credential is expired or revoked")
	}
	claims := AccessClaims{Issuer: "ai-native-devops-platform", Audience: "project-mcp", Subject: record.Binding.AgentID, TenantID: record.Binding.TenantID, ProjectID: record.Binding.ProjectID, UserID: record.Binding.UserID, AgentID: record.Binding.AgentID, CredentialID: record.CredentialID, Scopes: append([]string(nil), record.Binding.Scopes...), IssuedAt: now.Unix(), ExpiresAt: now.Add(AccessTTL).Unix(), JWTID: s.IDs.New("jwt")}
	token, err := s.Signer.Sign(claims)
	return token, claims, err
}

func (s *Service) VerifyAccess(token string, expected Binding) (AccessClaims, error) {
	if err := expected.Validate(); err != nil {
		return AccessClaims{}, err
	}
	claims, err := s.Signer.Verify(token)
	if err != nil {
		return AccessClaims{}, err
	}
	now := s.Clock.Now().UTC()
	if claims.Issuer != "ai-native-devops-platform" || claims.Audience != "project-mcp" || claims.ExpiresAt <= now.Unix() || claims.IssuedAt > now.Unix() || claims.ExpiresAt-claims.IssuedAt > int64(AccessTTL/time.Second) {
		return AccessClaims{}, errors.New("access credential is invalid or expired")
	}
	if claims.TenantID != expected.TenantID || claims.ProjectID != expected.ProjectID || claims.UserID != expected.UserID || claims.AgentID != expected.AgentID || claims.Subject != expected.AgentID {
		return AccessClaims{}, errors.New("access credential binding mismatch")
	}
	granted := make(map[string]struct{}, len(claims.Scopes))
	for _, scope := range claims.Scopes {
		granted[scope] = struct{}{}
	}
	for _, scope := range expected.Scopes {
		if _, ok := granted[scope]; !ok {
			return AccessClaims{}, errors.New("access credential scope mismatch")
		}
	}
	return claims, nil
}

// AuthenticateAccess verifies a signed access credential and binds it to the
// project selected by the trusted HTTP route. Tenant, user, agent, and scopes
// are accepted only from the signed claims, never from request arguments.
func (s *Service) AuthenticateAccess(token, projectID string) (AccessClaims, error) {
	if s == nil || s.Clock == nil || s.Signer == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(projectID) == "" {
		return AccessClaims{}, errors.New("access credential verification is unavailable")
	}
	claims, err := s.Signer.Verify(token)
	if err != nil {
		return AccessClaims{}, err
	}
	now := s.Clock.Now().UTC()
	binding := Binding{TenantID: claims.TenantID, ProjectID: claims.ProjectID, UserID: claims.UserID, AgentID: claims.AgentID, Scopes: claims.Scopes}
	if err := binding.Validate(); err != nil || claims.Issuer != "ai-native-devops-platform" || claims.Audience != "project-mcp" || claims.Subject != claims.AgentID || claims.ProjectID != projectID || claims.CredentialID == "" || claims.JWTID == "" || claims.ExpiresAt <= now.Unix() || claims.IssuedAt > now.Unix() || claims.ExpiresAt-claims.IssuedAt > int64(AccessTTL/time.Second) {
		return AccessClaims{}, errors.New("access credential is invalid, expired, or bound to another project")
	}
	return claims, nil
}

func (s *Service) Revoke(ctx context.Context, refreshToken string) error {
	return s.Store.RevokeRefresh(ctx, tokenHash(refreshToken), s.Clock.Now().UTC())
}

func (s *Service) IssueWorkspaceCertificate(ctx context.Context, accessToken string, expected Binding, workspaceID, taskID, csrPEM string) (WorkspaceCertificate, error) {
	if s == nil || s.WorkspaceCertificates == nil {
		return WorkspaceCertificate{}, errors.New("workspace certificate issuer is unavailable")
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(taskID) == "" || !strings.Contains(csrPEM, "BEGIN CERTIFICATE REQUEST") {
		return WorkspaceCertificate{}, errors.New("workspace certificate request is invalid")
	}
	claims, err := s.VerifyAccess(accessToken, expected)
	if err != nil {
		return WorkspaceCertificate{}, err
	}
	subject := WorkspaceCertificateSubject{TenantID: claims.TenantID, ProjectID: claims.ProjectID, AgentID: claims.AgentID, WorkspaceID: workspaceID, TaskID: taskID}
	certificate, err := s.WorkspaceCertificates.IssueWorkspaceCertificate(ctx, subject, csrPEM, AccessTTL)
	if err != nil {
		return WorkspaceCertificate{}, err
	}
	now := s.Clock.Now().UTC()
	if certificate.CertificateID == "" || certificate.CertificatePEM == "" || certificate.CAChainPEM == "" || !certificate.ExpiresAt.After(now) || certificate.ExpiresAt.After(now.Add(AccessTTL+time.Second)) {
		return WorkspaceCertificate{}, errors.New("workspace certificate issuer returned an invalid lease")
	}
	return certificate, nil
}

func (s *Service) require() error {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil || s.Secrets == nil || s.Signer == nil {
		return errors.New("agent enrollment dependencies are unavailable")
	}
	return nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type CryptoSecrets struct{}

func (CryptoSecrets) Secret(size int) (string, error) {
	if size < 32 || size > 128 {
		return "", errors.New("secret entropy size is invalid")
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type HMACSigner struct {
	Key []byte
}

func (s HMACSigner) Sign(claims AccessClaims) (string, error) {
	if len(s.Key) < 32 {
		return "", errors.New("JWT signing key must contain at least 256 bits")
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.Key)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s HMACSigner) Verify(token string) (AccessClaims, error) {
	if len(s.Key) < 32 {
		return AccessClaims{}, errors.New("JWT verification key must contain at least 256 bits")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return AccessClaims{}, errors.New("access JWT format is invalid")
	}
	mac := hmac.New(sha256.New, s.Key)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return AccessClaims{}, errors.New("access JWT signature is invalid")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return AccessClaims{}, errors.New("access JWT header is invalid")
	}
	var header map[string]string
	if err := json.Unmarshal(headerRaw, &header); err != nil || header["alg"] != "HS256" || header["typ"] != "JWT" {
		return AccessClaims{}, errors.New("access JWT algorithm is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AccessClaims{}, errors.New("access JWT payload is invalid")
	}
	var claims AccessClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return AccessClaims{}, fmt.Errorf("decode access JWT: %w", err)
	}
	return claims, nil
}
