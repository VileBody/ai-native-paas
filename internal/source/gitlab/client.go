package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
)

type Client struct {
	BaseURL    string
	AdminToken string
	HTTP       *http.Client
}
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gitlab api status %d: %s", e.Status, e.Message)
}
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}
func (c *Client) endpoint(path string) (string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		return "", errors.New("gitlab base url required")
	}
	if !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	return base + path, nil
}
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, mErr := json.Marshal(body)
		if mErr != nil {
			return mErr
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AdminToken != "" {
		req.Header.Set("PRIVATE-TOKEN", c.AdminToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Message any `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != nil {
			msg = fmt.Sprint(e.Message)
		}
		return &APIError{Status: resp.StatusCode, Message: redact(msg, c.AdminToken)}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type projectJSON struct {
	ID        int64 `json:"id"`
	Namespace struct {
		ID int64 `json:"id"`
	} `json:"namespace"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
	Description       string `json:"description"`
}

func toProvider(p projectJSON) application.ProviderRepository {
	return application.ProviderRepository{ID: p.ID, NamespaceID: p.Namespace.ID, Path: p.Path, PathWithNamespace: p.PathWithNamespace, WebURL: p.WebURL, DefaultBranch: p.DefaultBranch, Description: p.Description}
}
func (c *Client) CreateRepository(ctx context.Context, r application.CreateRepositoryRequest) (application.ProviderRepository, error) {
	description := "[paas-correlation:" + r.CorrelationID + "]"
	body := map[string]any{"namespace_id": r.NamespaceID, "name": r.Name, "path": r.Path, "initialize_with_readme": true, "default_branch": r.DefaultBranch, "visibility": "private", "description": description}
	var p projectJSON
	err := c.do(ctx, http.MethodPost, "/projects", body, &p)
	if err == nil {
		return toProvider(p), nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 409 {
		if found, ok, findErr := c.FindRepositoryByCorrelation(ctx, r.NamespaceID, r.CorrelationID); findErr == nil && ok {
			return found, nil
		}
	}
	return application.ProviderRepository{}, err
}
func (c *Client) FindRepositoryByCorrelation(ctx context.Context, namespaceID int64, correlationID string) (application.ProviderRepository, bool, error) {
	var projects []projectJSON
	path := "/groups/" + strconv.FormatInt(namespaceID, 10) + "/projects?per_page=100&simple=true&include_subgroups=false"
	if err := c.do(ctx, http.MethodGet, path, nil, &projects); err != nil {
		return application.ProviderRepository{}, false, err
	}
	marker := "[paas-correlation:" + correlationID + "]"
	for _, p := range projects {
		if strings.Contains(p.Description, marker) {
			return toProvider(p), true, nil
		}
	}
	return application.ProviderRepository{}, false, nil
}
func (c *Client) GetRepository(ctx context.Context, id int64) (application.ProviderRepository, error) {
	var p projectJSON
	if err := c.do(ctx, http.MethodGet, "/projects/"+strconv.FormatInt(id, 10), nil, &p); err != nil {
		return application.ProviderRepository{}, err
	}
	return toProvider(p), nil
}
func (c *Client) ProtectBranch(ctx context.Context, id int64, branch string) error {
	body := map[string]any{"name": branch, "push_access_level": 30, "merge_access_level": 30, "allow_force_push": false}
	err := c.do(ctx, http.MethodPost, "/projects/"+strconv.FormatInt(id, 10)+"/protected_branches", body, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 409 {
		return nil
	}
	return err
}
func (c *Client) GetBranchHead(ctx context.Context, id int64, branch string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	path := "/projects/" + strconv.FormatInt(id, 10) + "/repository/branches/" + url.PathEscape(branch)
	if err := c.do(ctx, http.MethodGet, path, nil, &b); err != nil {
		return "", err
	}
	if b.Commit.ID == "" {
		return "", errors.New("gitlab branch response has no commit")
	}
	return b.Commit.ID, nil
}
func (c *Client) CreateCredential(ctx context.Context, id int64, name string, expiresAt time.Time) (application.ProviderCredential, error) {
	body := map[string]any{"name": name, "scopes": []string{"read_repository", "write_repository"}, "access_level": 30, "expires_at": expiresAt.UTC().Format("2006-01-02")}
	var t struct {
		ID        int64  `json:"id"`
		Username  string `json:"user_name"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := c.do(ctx, http.MethodPost, "/projects/"+strconv.FormatInt(id, 10)+"/access_tokens", body, &t); err != nil {
		return application.ProviderCredential{}, err
	}
	return application.ProviderCredential{ID: strconv.FormatInt(t.ID, 10), Username: coalesce(t.Username, "oauth2"), Token: t.Token, ExpiresAt: expiresAt.UTC()}, nil
}
func (c *Client) RevokeCredential(ctx context.Context, id int64, credentialID string) error {
	err := c.do(ctx, http.MethodDelete, "/projects/"+strconv.FormatInt(id, 10)+"/access_tokens/"+url.PathEscape(credentialID), nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return nil
	}
	return err
}
func (c *Client) CreateMergeRequest(ctx context.Context, r application.CreateMergeRequestRequest) (application.ProviderMergeRequest, error) {
	body := map[string]any{"source_branch": r.SourceBranch, "target_branch": r.TargetBranch, "title": r.Title, "description": r.Description, "remove_source_branch": false}
	var mr struct {
		IID          int64  `json:"iid"`
		State        string `json:"state"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		WebURL       string `json:"web_url"`
		SHA          string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, "/projects/"+strconv.FormatInt(r.ProjectID, 10)+"/merge_requests", body, &mr); err != nil {
		return application.ProviderMergeRequest{}, err
	}
	return application.ProviderMergeRequest{IID: mr.IID, State: mr.State, SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, HeadSHA: mr.SHA, WebURL: mr.WebURL}, nil
}
func redact(v, secret string) string {
	if secret != "" {
		v = strings.ReplaceAll(v, secret, "[REDACTED]")
	}
	return v
}
func coalesce(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
