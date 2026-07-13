package kubeapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

const (
	defaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

type Config struct {
	Server             string
	Token              string
	HTTPClient         *http.Client
	FieldManager       string
	GatewayName        string
	GatewayNamespace   string
	GatewayPodLabels   map[string]string
	DNSNamespace       string
	DNSPodLabels       map[string]string
	RequestTimeout     time.Duration
	AllowedServerNames []string
}

type Client struct {
	server           *url.URL
	token            string
	http             *http.Client
	fieldManager     string
	gatewayName      string
	gatewayNamespace string
	gatewayPodLabels map[string]string
	dnsNamespace     string
	dnsPodLabels     map[string]string
	requestTimeout   time.Duration
}

func New(config Config) (*Client, error) {
	server, err := url.Parse(strings.TrimSpace(config.Server))
	if err != nil || server.Scheme == "" || server.Host == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "Kubernetes API server URL is invalid")
	}
	if server.Scheme != "https" && server.Scheme != "http" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "Kubernetes API server scheme is unsupported")
	}
	if len(config.AllowedServerNames) > 0 {
		allowed := false
		for _, name := range config.AllowedServerNames {
			if strings.EqualFold(server.Hostname(), strings.TrimSpace(name)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, domain.NewError(domain.CodeInvalidArgument, "Kubernetes API server is outside the allowlist")
		}
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	fieldManager := strings.TrimSpace(config.FieldManager)
	if fieldManager == "" {
		fieldManager = "paas-runtime-operator"
	}
	gatewayName := strings.TrimSpace(config.GatewayName)
	if gatewayName == "" {
		gatewayName = "platform-gateway"
	}
	gatewayNamespace := strings.TrimSpace(config.GatewayNamespace)
	if gatewayNamespace == "" {
		gatewayNamespace = "platform-gateway"
	}
	dnsNamespace := strings.TrimSpace(config.DNSNamespace)
	if dnsNamespace == "" {
		dnsNamespace = "kube-system"
	}
	gatewayLabels := cloneStringMap(config.GatewayPodLabels)
	if len(gatewayLabels) == 0 {
		gatewayLabels = map[string]string{"app.kubernetes.io/name": "gateway"}
	}
	dnsLabels := cloneStringMap(config.DNSPodLabels)
	if len(dnsLabels) == 0 {
		dnsLabels = map[string]string{"k8s-app": "kube-dns"}
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 30 * time.Second
	}
	return &Client{
		server:           server,
		token:            strings.TrimSpace(config.Token),
		http:             client,
		fieldManager:     fieldManager,
		gatewayName:      gatewayName,
		gatewayNamespace: gatewayNamespace,
		gatewayPodLabels: gatewayLabels,
		dnsNamespace:     dnsNamespace,
		dnsPodLabels:     dnsLabels,
		requestTimeout:   requestTimeout,
	}, nil
}

func InClusterConfig() (Config, error) {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	if port == "" {
		port = strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	}
	if host == "" || port == "" {
		return Config{}, domain.NewError(domain.CodeUnavailable, "in-cluster Kubernetes service environment is missing")
	}
	token, err := os.ReadFile(defaultTokenFile)
	if err != nil {
		return Config{}, domain.Wrap(domain.CodeUnavailable, "read Kubernetes service account token", err)
	}
	ca, err := os.ReadFile(defaultCAFile)
	if err != nil {
		return Config{}, domain.Wrap(domain.CodeUnavailable, "read Kubernetes CA bundle", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return Config{}, domain.NewError(domain.CodeUnavailable, "Kubernetes CA bundle contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: host}
	return Config{
		Server:         "https://" + host + ":" + port,
		Token:          strings.TrimSpace(string(token)),
		HTTPClient:     &http.Client{Transport: transport, Timeout: 30 * time.Second},
		RequestTimeout: 30 * time.Second,
	}, nil
}

func (c *Client) endpoint(parts ...string) string {
	u := *c.server
	segments := []string{strings.TrimSuffix(c.server.Path, "/")}
	for _, part := range parts {
		segments = append(segments, url.PathEscape(part))
	}
	u.Path = path.Join(segments...)
	return u.String()
}

func (c *Client) do(ctx context.Context, method, endpoint, contentType string, query url.Values, body any, out any) (bool, error) {
	if query != nil {
		u, err := url.Parse(endpoint)
		if err != nil {
			return false, domain.Wrap(domain.CodeInternal, "parse Kubernetes API endpoint", err)
		}
		u.RawQuery = query.Encode()
		endpoint = u.String()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return false, domain.Wrap(domain.CodeInternal, "encode Kubernetes API request", err)
		}
		reader = bytes.NewReader(raw)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, reader)
	if err != nil {
		return false, domain.Wrap(domain.CodeInternal, "create Kubernetes API request", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, domain.Wrap(domain.CodeUnavailable, "Kubernetes API request failed", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return false, domain.Wrap(domain.CodeUnavailable, "read Kubernetes API response", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := domain.CodeUnavailable
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			code = domain.CodeInvalidArgument
		case http.StatusForbidden, http.StatusUnauthorized:
			code = domain.CodePolicyRejected
		case http.StatusConflict:
			code = domain.CodeConflict
		case http.StatusTooManyRequests:
			code = domain.CodeCapacity
		}
		message := strings.TrimSpace(string(raw))
		if c.token != "" {
			message = strings.ReplaceAll(message, c.token, "[REDACTED]")
			message = strings.ReplaceAll(message, "Bearer "+c.token, "Bearer [REDACTED]")
		}
		if len(message) > 1024 {
			message = message[:1024]
		}
		return false, domain.NewError(code, fmt.Sprintf("Kubernetes API %s %s returned %d: %s", method, req.URL.Path, resp.StatusCode, message))
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return false, domain.Wrap(domain.CodeUnavailable, "decode Kubernetes API response", err)
		}
	}
	return true, nil
}

func (c *Client) apply(ctx context.Context, endpoint string, object any) error {
	query := url.Values{"fieldManager": {c.fieldManager}, "force": {"true"}}
	_, err := c.do(ctx, http.MethodPatch, endpoint, "application/apply-patch+yaml", query, object, nil)
	return err
}

func (c *Client) delete(ctx context.Context, endpoint string) error {
	_, err := c.do(ctx, http.MethodDelete, endpoint, "application/json", url.Values{"propagationPolicy": {"Background"}}, map[string]any{"apiVersion": "v1", "kind": "DeleteOptions"}, nil)
	return err
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func metadata(value kube.Metadata) map[string]any {
	out := map[string]any{"name": value.Name, "namespace": value.Namespace}
	if len(value.Labels) > 0 {
		out["labels"] = cloneStringMap(value.Labels)
	}
	if len(value.Annotations) > 0 {
		out["annotations"] = cloneStringMap(value.Annotations)
	}
	if len(value.OwnerReferences) > 0 {
		out["ownerReferences"] = value.OwnerReferences
	}
	return out
}

func labelSelector(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

func quantityMap(value runtimev1.ResourceQuantity) map[string]string {
	out := map[string]string{}
	if value.CPU != "" {
		out["cpu"] = value.CPU
	}
	if value.Memory != "" {
		out["memory"] = value.Memory
	}
	if value.EphemeralStorage != "" {
		out["ephemeral-storage"] = value.EphemeralStorage
	}
	return out
}

func int32ptr(value int) *int32 {
	v := int32(value)
	return &v
}

func failureThreshold(timeout int, period int) int {
	if period <= 0 {
		period = 2
	}
	if timeout <= 0 {
		timeout = 60
	}
	value := (timeout + period - 1) / period
	if value < 1 {
		return 1
	}
	if value > 300 {
		return 300
	}
	return value
}

func mapStringAny(value any) map[string]any {
	out, _ := value.(map[string]any)
	return out
}

func nested(value map[string]any, keys ...string) map[string]any {
	current := value
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func intValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	case int:
		return v
	}
	return 0
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func metadataFromObject(object map[string]any) kube.Metadata {
	meta := nested(object, "metadata")
	labels := map[string]string{}
	for key, value := range mapStringAny(meta["labels"]) {
		labels[key] = stringValue(value)
	}
	annotations := map[string]string{}
	for key, value := range mapStringAny(meta["annotations"]) {
		annotations[key] = stringValue(value)
	}
	return kube.Metadata{Name: stringValue(meta["name"]), Namespace: stringValue(meta["namespace"]), Labels: labels, Annotations: annotations, Generation: int64(intValue(meta["generation"]))}
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	return object, nil
}

func objectList(raw map[string]any) []map[string]any {
	items, _ := raw["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func statusConditionFailed(status map[string]any) (bool, string) {
	conditions, _ := status["conditions"].([]any)
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		typ := stringValue(condition["type"])
		value := stringValue(condition["status"])
		reason := stringValue(condition["reason"])
		message := stringValue(condition["message"])
		if (typ == "Progressing" && value == "False" && reason == "ProgressDeadlineExceeded") || (typ == "ReplicaFailure" && value == "True") {
			return true, firstNonEmpty(message, reason)
		}
	}
	return false, ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var (
	_ kube.Client                 = (*Client)(nil)
	_ application.RuntimeObserver = (*Client)(nil)
	_ interface {
		ListPaaSApps(context.Context) ([]runtimev1.PaaSApp, error)
	} = (*Client)(nil)
)
