package httpapi

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/workspace/session"
)

type PrincipalResolver interface {
	Resolve(*http.Request) (session.Principal, error)
}

// SPIFFEResolver accepts identity only from a TLS-verified client certificate
// URI SAN. Subject CN, headers, query parameters, and request bodies are never
// identity sources.
type SPIFFEResolver struct{ TrustDomain string }

func (r SPIFFEResolver) Resolve(request *http.Request) (session.Principal, error) {
	if request == nil || request.TLS == nil || len(request.TLS.PeerCertificates) == 0 || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.VerifiedChains[0]) == 0 || strings.TrimSpace(r.TrustDomain) == "" {
		return session.Principal{}, errors.New("verified workspace client certificate is required")
	}
	leaf := request.TLS.PeerCertificates[0]
	if len(leaf.Raw) == 0 || !sameCertificate(leaf, request.TLS.VerifiedChains[0][0]) || !allowsClientAuthentication(leaf) || len(leaf.URIs) != 1 {
		return session.Principal{}, errors.New("workspace client certificate is invalid")
	}
	uri := leaf.URIs[0]
	if uri.Scheme != "spiffe" || uri.Host != r.TrustDomain || uri.User != nil || uri.Port() != "" || uri.RawQuery != "" || uri.Fragment != "" {
		return session.Principal{}, errors.New("workspace client certificate trust domain is invalid")
	}
	segments := strings.Split(strings.Trim(uri.EscapedPath(), "/"), "/")
	if len(segments) != 10 || segments[0] != "tenant" || segments[2] != "project" || segments[4] != "workspace" || segments[6] != "task" || segments[8] != "agent" {
		return session.Principal{}, errors.New("workspace client certificate URI is invalid")
	}
	values := make([]string, 5)
	for index, source := range []string{segments[1], segments[3], segments[5], segments[7], segments[9]} {
		value, err := url.PathUnescape(source)
		if err != nil || value == "" || strings.Contains(value, "/") {
			return session.Principal{}, errors.New("workspace client certificate URI is invalid")
		}
		values[index] = value
	}
	digest := sha256.Sum256(leaf.Raw)
	return session.Principal{
		TenantID: values[0], ProjectID: values[1], WorkspaceID: values[2], TaskID: values[3], AgentID: values[4],
		CertificateID: "sha256:" + hex.EncodeToString(digest[:]), NotAfter: leaf.NotAfter.UTC(),
	}, nil
}

func sameCertificate(left, right *x509.Certificate) bool {
	if left == nil || right == nil || len(left.Raw) != len(right.Raw) {
		return false
	}
	return string(left.Raw) == string(right.Raw)
}

func allowsClientAuthentication(certificate *x509.Certificate) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}
