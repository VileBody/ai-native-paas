// Package servicemtls provides the verified TLS boundary used by internal
// control-plane services. A client certificate authenticates the service;
// httpauth then binds the request to an explicit tenant/project delegation.
package servicemtls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
)

const maximumPEMBytes = 256 << 10

type Verifier struct {
	TrustDomain     string
	AllowedServices map[string]struct{}
	Scopes          []string
	Now             func() time.Time
}

type ServerConfig struct {
	Address           string
	CertificateFile   string
	PrivateKeyFile    string
	ClientCAFile      string
	TrustDomain       string
	AllowedServices   string
	Scopes            string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

func ServerConfigFromEnv(defaultAllowedServices, defaultScopes string) ServerConfig {
	return ServerConfig{
		Address:         env("INTERNAL_MTLS_ADDR", ":8443"),
		CertificateFile: strings.TrimSpace(os.Getenv("INTERNAL_MTLS_SERVER_CERT_FILE")),
		PrivateKeyFile:  strings.TrimSpace(os.Getenv("INTERNAL_MTLS_SERVER_KEY_FILE")),
		ClientCAFile:    strings.TrimSpace(os.Getenv("INTERNAL_MTLS_CLIENT_CA_FILE")),
		TrustDomain:     strings.TrimSpace(os.Getenv("INTERNAL_MTLS_TRUST_DOMAIN")),
		AllowedServices: env("INTERNAL_MTLS_ALLOWED_CLIENTS", defaultAllowedServices),
		Scopes:          env("INTERNAL_MTLS_SCOPES", defaultScopes),
	}
}

func NewVerifier(trustDomain, allowedServices, scopes string) (*Verifier, error) {
	trustDomain = strings.TrimSpace(trustDomain)
	if trustDomain == "" || strings.ContainsAny(trustDomain, "/:@ \\") {
		return nil, errors.New("internal mTLS trust domain is invalid")
	}
	allowed := splitSet(allowedServices)
	if len(allowed) == 0 {
		return nil, errors.New("internal mTLS client allowlist is required")
	}
	for service := range allowed {
		if !validSegment(service) {
			return nil, errors.New("internal mTLS client allowlist is invalid")
		}
	}
	granted := splitList(scopes)
	if len(granted) == 0 {
		return nil, errors.New("internal mTLS service scopes are required")
	}
	return &Verifier{TrustDomain: trustDomain, AllowedServices: allowed, Scopes: granted, Now: time.Now}, nil
}

func (v *Verifier) VerifyMTLS(_ context.Context, certificate *x509.Certificate) (httpauth.Identity, error) {
	if v == nil || certificate == nil || len(certificate.URIs) != 1 || len(v.AllowedServices) == 0 {
		return httpauth.Identity{}, errors.New("verified internal service certificate is required")
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) || !allowsClientAuth(certificate.ExtKeyUsage) {
		return httpauth.Identity{}, errors.New("internal service certificate is expired or has invalid usage")
	}
	identityURI := certificate.URIs[0]
	service, err := serviceFromURI(identityURI, v.TrustDomain)
	if err != nil {
		return httpauth.Identity{}, err
	}
	if _, ok := v.AllowedServices[service]; !ok {
		return httpauth.Identity{}, errors.New("internal service is not allowed")
	}
	return httpauth.Identity{SubjectID: service, Scopes: append([]string(nil), v.Scopes...)}, nil
}

func NewClient(caFile, certificateFile, privateKeyFile string) (*http.Client, error) {
	roots, err := loadCertPool(caFile)
	if err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(strings.TrimSpace(certificateFile), strings.TrimSpace(privateKeyFile))
	if err != nil {
		return nil, errors.New("load internal mTLS client certificate")
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate},
		}},
	}, nil
}

func NewServerTLSConfig(certificateFile, privateKeyFile, clientCAFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(strings.TrimSpace(certificateFile), strings.TrimSpace(privateKeyFile))
	if err != nil {
		return nil, errors.New("load internal mTLS server certificate")
	}
	clientCAs, err := loadCertPool(clientCAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate},
		ClientCAs: clientCAs, ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}

func NewServer(config ServerConfig, handler http.Handler) (*http.Server, error) {
	if strings.TrimSpace(config.Address) == "" || handler == nil {
		return nil, errors.New("internal mTLS server address and handler are required")
	}
	verifier, err := NewVerifier(config.TrustDomain, config.AllowedServices, config.Scopes)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := NewServerTLSConfig(config.CertificateFile, config.PrivateKeyFile, config.ClientCAFile)
	if err != nil {
		return nil, err
	}
	readHeaderTimeout := config.ReadHeaderTimeout
	if readHeaderTimeout <= 0 {
		readHeaderTimeout = 5 * time.Second
	}
	readTimeout := config.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 30 * time.Second
	}
	writeTimeout := config.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 30 * time.Second
	}
	idleTimeout := config.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Second
	}
	return &http.Server{
		Addr: strings.TrimSpace(config.Address), TLSConfig: tlsConfig,
		Handler:           httpauth.Middleware{Profile: "production", MTLS: verifier}.Wrap(handler),
		ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout,
	}, nil
}

func serviceFromURI(identity *url.URL, trustDomain string) (string, error) {
	if identity == nil || identity.Scheme != "spiffe" || identity.Host != trustDomain || identity.User != nil || identity.Port() != "" || identity.RawQuery != "" || identity.Fragment != "" || identity.RawPath != "" {
		return "", errors.New("internal service SPIFFE identity is invalid")
	}
	const prefix = "/service/"
	if !strings.HasPrefix(identity.Path, prefix) {
		return "", errors.New("internal service SPIFFE identity is invalid")
	}
	service := strings.TrimPrefix(identity.Path, prefix)
	if !validSegment(service) {
		return "", errors.New("internal service SPIFFE identity is invalid")
	}
	return service, nil
}

func allowsClientAuth(usages []x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func loadCertPool(filename string) (*x509.CertPool, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, errors.New("internal mTLS CA file is required")
	}
	raw, err := os.ReadFile(filename)
	if err != nil || len(raw) == 0 || len(raw) > maximumPEMBytes {
		return nil, errors.New("load internal mTLS CA")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("parse internal mTLS CA")
	}
	return pool, nil
}

func splitSet(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range splitList(raw) {
		out[value] = struct{}{}
	}
	return out
}

func splitList(raw string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validSegment(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '-' && i > 0 && i < len(value)-1) {
			continue
		}
		return false
	}
	return true
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
