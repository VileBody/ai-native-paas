package openbao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maximumSecretBytes   = 64 << 10
	maximumResponseBytes = 128 << 10
	maximumTokenBytes    = 16 << 10
)

var (
	mountPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	pathSegmentRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	errInvalidReply = errors.New("OpenBao KV response is invalid")
)

type KVV2Config struct {
	Address    string
	TokenFile  string
	Mount      string
	HTTPClient *http.Client
}

// KVV2Backend stores write-only attachment values in an OpenBao KV v2 mount.
// Delete targets the metadata endpoint so every version is permanently
// removed. It intentionally does not implement PrefixBackend: KV v2 has no
// atomic prefix-delete primitive, so credential-prefix revocation must fail
// closed or use a lease-aware credential engine.
type KVV2Backend struct {
	base      *url.URL
	tokenFile string
	mount     string
	client    *http.Client
}

var _ Backend = (*KVV2Backend)(nil)

func NewKVV2Backend(config KVV2Config) (*KVV2Backend, error) {
	base, err := url.Parse(strings.TrimSpace(config.Address))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return nil, errors.New("OpenBao address is invalid")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && isLoopbackHost(base.Hostname())) {
		return nil, errors.New("OpenBao address requires HTTPS")
	}
	if strings.TrimSpace(config.TokenFile) == "" || !mountPattern.MatchString(config.Mount) {
		return nil, errors.New("OpenBao KV configuration is incomplete")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &KVV2Backend{base: base, tokenFile: config.TokenFile, mount: config.Mount, client: client}, nil
}

func (b *KVV2Backend) Put(ctx context.Context, secretPath string, value []byte, expiresAt time.Time) (string, error) {
	if b == nil || b.base == nil || b.client == nil || len(value) == 0 || len(value) > maximumSecretBytes || !validSecretPath(secretPath) {
		return "", errors.New("OpenBao KV write is invalid")
	}
	payload := struct {
		Data struct {
			Value     string `json:"value"`
			Encoding  string `json:"encoding"`
			ExpiresAt string `json:"expires_at,omitempty"`
		} `json:"data"`
	}{}
	payload.Data.Value = base64.StdEncoding.EncodeToString(value)
	payload.Data.Encoding = "base64"
	if !expiresAt.IsZero() {
		payload.Data.ExpiresAt = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	status, raw, err := b.do(ctx, http.MethodPost, "data", secretPath, payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", responseStatusError(status)
	}
	var response struct {
		Data struct {
			Version int64 `json:"version"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Data.Version <= 0 {
		return "", errInvalidReply
	}
	return strconv.FormatInt(response.Data.Version, 10), nil
}

func (b *KVV2Backend) Metadata(ctx context.Context, secretPath string) (Meta, error) {
	if b == nil || b.base == nil || b.client == nil || !validSecretPath(secretPath) {
		return Meta{}, errors.New("OpenBao KV metadata request is invalid")
	}
	status, raw, err := b.do(ctx, http.MethodGet, "metadata", secretPath, nil)
	if err != nil {
		return Meta{}, err
	}
	if status == http.StatusNotFound {
		return Meta{}, nil
	}
	if status != http.StatusOK {
		return Meta{}, responseStatusError(status)
	}
	var response struct {
		Data struct {
			CurrentVersion int64 `json:"current_version"`
			Versions       map[string]struct {
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
			} `json:"versions"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Data.CurrentVersion <= 0 {
		return Meta{}, errInvalidReply
	}
	version := strconv.FormatInt(response.Data.CurrentVersion, 10)
	latest, ok := response.Data.Versions[version]
	if !ok {
		return Meta{}, errInvalidReply
	}
	return Meta{Version: version, Exists: !latest.Destroyed && strings.TrimSpace(latest.DeletionTime) == ""}, nil
}

func (b *KVV2Backend) Delete(ctx context.Context, secretPath string) error {
	if b == nil || b.base == nil || b.client == nil || !validSecretPath(secretPath) {
		return errors.New("OpenBao KV delete is invalid")
	}
	status, _, err := b.do(ctx, http.MethodDelete, "metadata", secretPath, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNoContent || status == http.StatusNotFound {
		return nil
	}
	return responseStatusError(status)
}

func (b *KVV2Backend) do(ctx context.Context, method, endpointKind, secretPath string, payload any) (int, []byte, error) {
	token, err := readWorkloadToken(b.tokenFile)
	if err != nil {
		return 0, nil, err
	}
	defer zeroBytes(token)

	var body io.Reader
	if payload != nil {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil || len(raw) > maximumResponseBytes {
			return 0, nil, errors.New("OpenBao KV request is invalid")
		}
		body = bytes.NewReader(raw)
	}
	endpoint := *b.base
	endpoint.Path = pathpkg.Join("/v1", b.mount, endpointKind, secretPath)
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return 0, nil, errors.New("create OpenBao KV request")
	}
	request.Header.Set("X-Vault-Token", string(token))
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: OpenBao KV request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(raw) > maximumResponseBytes {
		return 0, nil, errInvalidReply
	}
	return response.StatusCode, raw, nil
}

func validSecretPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value != strings.Trim(value, "/") || len(value) > 1024 || strings.ContainsAny(value, "\\\x00\r\n") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if !pathSegmentRE.MatchString(segment) || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func responseStatusError(status int) error {
	if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
		return fmt.Errorf("%w: OpenBao KV status %d", ErrUnavailable, status)
	}
	return fmt.Errorf("OpenBao KV request rejected with status %d", status)
}

func readWorkloadToken(filename string) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("open OpenBao workload token")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !secureTokenMode(info.Mode()) || info.Size() < 16 || info.Size() > maximumTokenBytes {
		return nil, errors.New("OpenBao workload token file is insecure")
	}
	token, err := io.ReadAll(io.LimitReader(file, maximumTokenBytes+1))
	if err != nil || len(token) > maximumTokenBytes {
		zeroBytes(token)
		return nil, errors.New("read OpenBao workload token")
	}
	token = bytes.TrimSpace(token)
	if len(token) < 16 || bytes.IndexByte(token, 0) >= 0 {
		zeroBytes(token)
		return nil, errors.New("OpenBao workload token is invalid")
	}
	return token, nil
}

func secureTokenMode(mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}
	permissions := mode.Perm()
	return permissions&0o400 != 0 && permissions&0o137 == 0
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
