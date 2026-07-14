// Package openbao issues narrow, short-lived workspace client certificates.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspace/bootstrap"
)

const maximumCertificateTTL = 15 * time.Minute

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type Config struct {
	Address     string
	TokenFile   string
	PKIMount    string
	Role        string
	TrustDomain string
	TTL         time.Duration
	HTTPClient  *http.Client
	Clock       workspace.Clock
}

type Issuer struct {
	base        *url.URL
	tokenFile   string
	mount       string
	role        string
	trustDomain string
	ttl         time.Duration
	client      *http.Client
	clock       workspace.Clock
}

func NewIssuer(config Config) (*Issuer, error) {
	base, err := url.Parse(strings.TrimSpace(config.Address))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return nil, errors.New("OpenBao address is invalid")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname())) {
		return nil, errors.New("OpenBao address requires HTTPS")
	}
	if strings.TrimSpace(config.TokenFile) == "" || !namePattern.MatchString(config.PKIMount) || !namePattern.MatchString(config.Role) || strings.TrimSpace(config.TrustDomain) == "" || strings.ContainsAny(config.TrustDomain, "/:@") || config.Clock == nil {
		return nil, errors.New("OpenBao workspace issuer configuration is incomplete")
	}
	ttl := config.TTL
	if ttl < time.Minute || ttl > maximumCertificateTTL {
		return nil, errors.New("OpenBao workspace certificate TTL is invalid")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Issuer{base: base, tokenFile: config.TokenFile, mount: config.PKIMount, role: config.Role, trustDomain: config.TrustDomain, ttl: ttl, client: client, clock: config.Clock}, nil
}

func (i *Issuer) Issue(ctx context.Context, identity bootstrap.Identity) (bootstrap.CertificateBundle, error) {
	if i == nil || i.base == nil || i.client == nil || i.clock == nil {
		return bootstrap.CertificateBundle{}, errors.New("OpenBao workspace issuer is unavailable")
	}
	identityURI, err := identity.SPIFFEURI(i.trustDomain)
	if err != nil {
		return bootstrap.CertificateBundle{}, err
	}
	token, err := readToken(i.tokenFile)
	if err != nil {
		return bootstrap.CertificateBundle{}, err
	}
	defer clear(token)
	payload, err := json.Marshal(map[string]any{
		"uri_sans": identityURI.String(), "exclude_cn_from_sans": true, "ttl": i.ttl.String(),
	})
	if err != nil {
		return bootstrap.CertificateBundle{}, err
	}
	endpoint := *i.base
	endpoint.Path = path.Join("/v1", i.mount, "issue", i.role)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return bootstrap.CertificateBundle{}, errors.New("create OpenBao PKI request")
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	response, err := i.client.Do(request)
	if err != nil {
		return bootstrap.CertificateBundle{}, errors.New("OpenBao PKI request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return bootstrap.CertificateBundle{}, fmt.Errorf("OpenBao PKI issuance failed with status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(raw) == 0 || len(raw) > 512<<10 {
		return bootstrap.CertificateBundle{}, errors.New("OpenBao PKI response is invalid")
	}
	var envelope struct {
		Data struct {
			Certificate  string   `json:"certificate"`
			PrivateKey   string   `json:"private_key"`
			IssuingCA    string   `json:"issuing_ca"`
			CAChain      []string `json:"ca_chain"`
			SerialNumber string   `json:"serial_number"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return bootstrap.CertificateBundle{}, errors.New("OpenBao PKI response is invalid")
	}
	chain := strings.Join(envelope.Data.CAChain, "\n")
	if strings.TrimSpace(chain) == "" {
		chain = envelope.Data.IssuingCA
	}
	bundle := bootstrap.CertificateBundle{
		Certificate: []byte(envelope.Data.Certificate), PrivateKey: []byte(envelope.Data.PrivateKey), CAChain: []byte(chain),
		IdentityURI: identityURI.String(), Serial: envelope.Data.SerialNumber,
	}
	if err := validateBundle(&bundle, identityURI, i.clock.Now().UTC(), i.ttl); err != nil {
		bundle.Clear()
		return bootstrap.CertificateBundle{}, err
	}
	return bundle, nil
}

// Revoke implements workspace.LeaseRevoker using the synchronous OpenBao
// lease endpoint. The reason is intentionally not sent to the provider; it is
// recorded by the workspace audit transaction at the control-plane boundary.
func (i *Issuer) Revoke(ctx context.Context, leaseID, reason string) error {
	if i == nil || i.base == nil || i.client == nil || strings.TrimSpace(leaseID) == "" || len(leaseID) > 512 || strings.TrimSpace(reason) == "" {
		return errors.New("OpenBao lease revocation is invalid")
	}
	token, err := readToken(i.tokenFile)
	if err != nil {
		return err
	}
	defer clear(token)
	payload, err := json.Marshal(map[string]any{"lease_id": leaseID, "sync": true})
	if err != nil {
		return err
	}
	endpoint := *i.base
	endpoint.Path = "/v1/sys/leases/revoke"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return errors.New("create OpenBao lease request")
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	response, err := i.client.Do(request)
	if err != nil {
		return errors.New("OpenBao lease revocation request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("OpenBao lease revocation failed with status %d", response.StatusCode)
	}
	return nil
}

func validateBundle(bundle *bootstrap.CertificateBundle, expected *url.URL, now time.Time, ttl time.Duration) error {
	if bundle == nil || strings.TrimSpace(bundle.Serial) == "" {
		return errors.New("OpenBao workspace certificate response is incomplete")
	}
	pair, err := tls.X509KeyPair(bundle.Certificate, bundle.PrivateKey)
	if err != nil || len(pair.Certificate) != 1 {
		return errors.New("OpenBao workspace certificate/key pair is invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != expected.String() || !hasUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) || hasUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return errors.New("OpenBao workspace certificate identity is invalid")
	}
	if leaf.NotBefore.After(now.Add(time.Minute)) || !leaf.NotAfter.After(now.Add(time.Minute)) || leaf.NotAfter.After(now.Add(ttl+time.Minute)) {
		return errors.New("OpenBao workspace certificate lifetime is invalid")
	}
	roots := x509.NewCertPool()
	remaining := bundle.CAChain
	added := 0
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		remaining = rest
		if block == nil {
			break
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil || !certificate.IsCA {
			return errors.New("OpenBao workspace CA chain is invalid")
		}
		roots.AddCert(certificate)
		added++
	}
	if added == 0 {
		return errors.New("OpenBao workspace CA chain is invalid")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return errors.New("OpenBao workspace certificate chain is invalid")
	}
	bundle.NotAfter = leaf.NotAfter.UTC()
	return nil
}

func readToken(filename string) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("open OpenBao workload token")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 16 || info.Size() > 16<<10 {
		return nil, errors.New("OpenBao workload token file is insecure")
	}
	token, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(token) > 16<<10 {
		return nil, errors.New("read OpenBao workload token")
	}
	token = bytes.TrimSpace(token)
	if len(token) < 16 {
		return nil, errors.New("OpenBao workload token is invalid")
	}
	return token, nil
}

func hasUsage(values []x509.ExtKeyUsage, target x509.ExtKeyUsage) bool {
	for _, value := range values {
		if value == target || value == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
