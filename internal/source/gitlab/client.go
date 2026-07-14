package gitlab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
)

type Client struct {
	BaseURL             string
	AdminToken          string
	HTTP                *http.Client
	MaxRateLimitRetries int
	MaxRateLimitDelay   time.Duration
	Sleep               func(context.Context, time.Duration) error
}
type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
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
	var requestBody []byte
	if body != nil {
		requestBody, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	retries := 0
	if method == http.MethodGet || method == http.MethodHead {
		retries = c.MaxRateLimitRetries
		if retries <= 0 {
			retries = 3
		}
	}
	for attempt := 0; ; attempt++ {
		err = c.doOnce(ctx, method, endpoint, requestBody, body != nil, out)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || attempt >= retries {
			return err
		}
		delay := apiErr.RetryAfter
		if delay <= 0 {
			delay = time.Duration(1<<min(attempt, 5)) * 200 * time.Millisecond
		}
		limit := c.MaxRateLimitDelay
		if limit <= 0 {
			limit = 30 * time.Second
		}
		if delay > limit {
			delay = limit
		}
		if err = c.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

func (c *Client) doOnce(ctx context.Context, method, endpoint string, requestBody []byte, hasBody bool, out any) error {
	var reader io.Reader
	if hasBody {
		reader = bytes.NewReader(requestBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if hasBody {
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
		return &APIError{Status: resp.StatusCode, Message: redact(msg, c.AdminToken), RetryAfter: retryAfter(resp.Header, time.Now().UTC())}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) sleep(ctx context.Context, delay time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfter(header http.Header, now time.Time) time.Duration {
	if value := strings.TrimSpace(header.Get("RateLimit-ResetTime")); value != "" {
		if reset, err := http.ParseTime(value); err == nil && reset.After(now) {
			return reset.Sub(now)
		}
	}
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if reset, err := http.ParseTime(value); err == nil && reset.After(now) {
			return reset.Sub(now)
		}
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type projectJSON struct {
	ID        int64 `json:"id"`
	Namespace struct {
		ID int64 `json:"id"`
	} `json:"namespace"`
	Path              string   `json:"path"`
	PathWithNamespace string   `json:"path_with_namespace"`
	WebURL            string   `json:"web_url"`
	DefaultBranch     string   `json:"default_branch"`
	Description       string   `json:"description"`
	Topics            []string `json:"topics"`
	Archived          bool     `json:"archived"`
}

func toProvider(p projectJSON) application.ProviderRepository {
	return application.ProviderRepository{ID: p.ID, NamespaceID: p.Namespace.ID, Path: p.Path, PathWithNamespace: p.PathWithNamespace, WebURL: p.WebURL, DefaultBranch: p.DefaultBranch, Description: p.Description, ExternalID: projectExternalID(p.Description), Topics: append([]string(nil), p.Topics...), Archived: p.Archived}
}
func (c *Client) CreateRepository(ctx context.Context, r application.CreateRepositoryRequest) (application.ProviderRepository, error) {
	description := "[paas-correlation:" + r.CorrelationID + "]"
	body := map[string]any{"namespace_id": r.NamespaceID, "name": r.Name, "path": r.Path, "initialize_with_readme": true, "default_branch": r.DefaultBranch, "visibility": "private", "description": description, "topics": []string{"ai-native-paas"}}
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
func (c *Client) ListRepositoriesByNamespace(ctx context.Context, namespaceID int64) ([]application.ProviderRepository, error) {
	if namespaceID <= 0 {
		return nil, errors.New("gitlab namespace id is invalid")
	}
	const perPage = 100
	var result []application.ProviderRepository
	for page := 1; page <= 100; page++ {
		var projects []projectJSON
		path := "/groups/" + strconv.FormatInt(namespaceID, 10) + "/projects?include_subgroups=false&order_by=id&page=" + strconv.Itoa(page) + "&per_page=" + strconv.Itoa(perPage) + "&sort=asc&with_shared=false"
		if err := c.do(ctx, http.MethodGet, path, nil, &projects); err != nil {
			return nil, err
		}
		for _, project := range projects {
			if project.Namespace.ID == namespaceID {
				result = append(result, toProvider(project))
			}
		}
		if len(projects) < perPage {
			return result, nil
		}
	}
	return nil, errors.New("gitlab namespace project listing exceeded page limit")
}
func (c *Client) FindRepositoryByCorrelation(ctx context.Context, namespaceID int64, correlationID string) (application.ProviderRepository, bool, error) {
	projects, err := c.ListRepositoriesByNamespace(ctx, namespaceID)
	if err != nil {
		return application.ProviderRepository{}, false, err
	}
	for _, p := range projects {
		if p.ExternalID == correlationID && gitlabTopicPresent(p.Topics, "ai-native-paas") {
			return p, true, nil
		}
	}
	return application.ProviderRepository{}, false, nil
}

func gitlabTopicPresent(topics []string, expected string) bool {
	for _, topic := range topics {
		if strings.EqualFold(strings.TrimSpace(topic), expected) {
			return true
		}
	}
	return false
}

func projectExternalID(description string) string {
	const prefix = "[paas-correlation:"
	start := strings.Index(description, prefix)
	if start < 0 {
		return ""
	}
	value := description[start+len(prefix):]
	end := strings.IndexByte(value, ']')
	if end <= 0 || end > 128 {
		return ""
	}
	value = value[:end]
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return ""
		}
	}
	return value
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
		Description  string `json:"description"`
	}
	if err := c.do(ctx, http.MethodPost, "/projects/"+strconv.FormatInt(r.ProjectID, 10)+"/merge_requests", body, &mr); err != nil {
		return application.ProviderMergeRequest{}, err
	}
	return application.ProviderMergeRequest{IID: mr.IID, State: mr.State, SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, HeadSHA: mr.SHA, WebURL: mr.WebURL, Description: mr.Description}, nil
}

func (c *Client) FindOpenMergeRequest(ctx context.Context, projectID int64, sourceBranch, targetBranch string) (application.ProviderMergeRequest, bool, error) {
	path := "/projects/" + strconv.FormatInt(projectID, 10) + "/merge_requests?state=opened&source_branch=" + url.QueryEscape(sourceBranch) + "&target_branch=" + url.QueryEscape(targetBranch) + "&per_page=2"
	var values []struct {
		IID          int64  `json:"iid"`
		State        string `json:"state"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		WebURL       string `json:"web_url"`
		SHA          string `json:"sha"`
		Description  string `json:"description"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &values); err != nil {
		return application.ProviderMergeRequest{}, false, err
	}
	for _, value := range values {
		if value.SourceBranch == sourceBranch && value.TargetBranch == targetBranch {
			return application.ProviderMergeRequest{
				IID: value.IID, State: value.State, SourceBranch: value.SourceBranch,
				TargetBranch: value.TargetBranch, HeadSHA: value.SHA, WebURL: value.WebURL, Description: value.Description,
			}, true, nil
		}
	}
	return application.ProviderMergeRequest{}, false, nil
}

func (c *Client) CreateMergeRequestNote(ctx context.Context, projectID, mergeRequestIID int64, body string) (application.ProviderMergeRequestNote, error) {
	if projectID <= 0 || mergeRequestIID <= 0 || strings.TrimSpace(body) == "" || len(body) > 16<<10 {
		return application.ProviderMergeRequestNote{}, errors.New("gitlab merge request note request is invalid")
	}
	var note struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	path := "/projects/" + strconv.FormatInt(projectID, 10) + "/merge_requests/" + strconv.FormatInt(mergeRequestIID, 10) + "/notes"
	if err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body}, &note); err != nil {
		return application.ProviderMergeRequestNote{}, err
	}
	if note.ID <= 0 || note.Body == "" {
		return application.ProviderMergeRequestNote{}, errors.New("gitlab merge request note response is invalid")
	}
	return application.ProviderMergeRequestNote{ID: note.ID, Body: note.Body}, nil
}

func (c *Client) FindMergeRequestNoteByMarker(ctx context.Context, projectID, mergeRequestIID int64, marker string) (application.ProviderMergeRequestNote, bool, error) {
	if projectID <= 0 || mergeRequestIID <= 0 || strings.TrimSpace(marker) == "" || len(marker) > 256 || strings.ContainsAny(marker, "\r\n") {
		return application.ProviderMergeRequestNote{}, false, errors.New("gitlab merge request note marker is invalid")
	}
	var notes []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	path := "/projects/" + strconv.FormatInt(projectID, 10) + "/merge_requests/" + strconv.FormatInt(mergeRequestIID, 10) + "/notes?sort=desc&order_by=created_at&per_page=100"
	if err := c.do(ctx, http.MethodGet, path, nil, &notes); err != nil {
		return application.ProviderMergeRequestNote{}, false, err
	}
	for _, note := range notes {
		if note.ID > 0 && strings.HasPrefix(note.Body, marker+"\n") {
			return application.ProviderMergeRequestNote{ID: note.ID, Body: note.Body}, true, nil
		}
	}
	return application.ProviderMergeRequestNote{}, false, nil
}

func (c *Client) ArchiveRepository(ctx context.Context, projectID int64) (application.ProviderRepository, error) {
	return c.setRepositoryArchived(ctx, projectID, true)
}

func (c *Client) UnarchiveRepository(ctx context.Context, projectID int64) (application.ProviderRepository, error) {
	return c.setRepositoryArchived(ctx, projectID, false)
}

func (c *Client) setRepositoryArchived(ctx context.Context, projectID int64, archived bool) (application.ProviderRepository, error) {
	if projectID <= 0 {
		return application.ProviderRepository{}, errors.New("gitlab project id is invalid")
	}
	action := "archive"
	if !archived {
		action = "unarchive"
	}
	var project projectJSON
	path := "/projects/" + strconv.FormatInt(projectID, 10) + "/" + action
	if err := c.do(ctx, http.MethodPost, path, nil, &project); err != nil {
		return application.ProviderRepository{}, err
	}
	if project.ID != projectID || project.Archived != archived {
		return application.ProviderRepository{}, errors.New("gitlab archive response does not match requested state")
	}
	return toProvider(project), nil
}

func (c *Client) DeleteRepository(ctx context.Context, projectID int64) error {
	if projectID <= 0 {
		return errors.New("gitlab project id is invalid")
	}
	err := c.do(ctx, http.MethodDelete, "/projects/"+strconv.FormatInt(projectID, 10), nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *Client) BootstrapRepository(ctx context.Context, request application.BootstrapRepositoryRequest) (string, error) {
	if request.ProviderProjectID <= 0 || strings.TrimSpace(request.Branch) == "" || strings.TrimSpace(request.ExpectedBaseSHA) == "" || strings.TrimSpace(request.CommitMessage) == "" || len(request.Files) == 0 || len(request.Files) > 100 {
		return "", errors.New("gitlab bootstrap request is invalid")
	}
	actions := make([]map[string]any, 0, len(request.Files))
	seen := make(map[string]struct{}, len(request.Files))
	total := 0
	for _, file := range request.Files {
		file.Path = strings.TrimSpace(file.Path)
		cleaned := pathpkg.Clean(file.Path)
		if file.Path == "" || cleaned != file.Path || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, `\`) || strings.Contains(file.Path, "\x00") {
			return "", errors.New("gitlab bootstrap file path is invalid")
		}
		if _, exists := seen[file.Path]; exists {
			return "", errors.New("gitlab bootstrap file path is duplicated")
		}
		seen[file.Path] = struct{}{}
		total += len(file.Content)
		if total > 4<<20 {
			return "", errors.New("gitlab bootstrap content is too large")
		}
		action := "create"
		if file.Update {
			action = "update"
		}
		actions = append(actions, map[string]any{
			"action": action, "file_path": file.Path, "content": base64.StdEncoding.EncodeToString(file.Content),
			"encoding": "base64", "execute_filemode": file.Executable,
		})
	}
	body := map[string]any{
		"branch": request.Branch, "start_sha": request.ExpectedBaseSHA,
		"commit_message": request.CommitMessage, "actions": actions,
	}
	var commit struct {
		ID string `json:"id"`
	}
	path := "/projects/" + strconv.FormatInt(request.ProviderProjectID, 10) + "/repository/commits"
	if err := c.do(ctx, http.MethodPost, path, body, &commit); err == nil {
		if commit.ID == "" {
			return "", errors.New("gitlab bootstrap commit response has no id")
		}
		return commit.ID, nil
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || (apiErr.Status != http.StatusBadRequest && apiErr.Status != http.StatusConflict) {
			return "", err
		}
		matches, verifyErr := c.bootstrapFilesMatch(ctx, request)
		if verifyErr != nil || !matches {
			return "", err
		}
	}
	return c.GetBranchHead(ctx, request.ProviderProjectID, request.Branch)
}

func (c *Client) bootstrapFilesMatch(ctx context.Context, request application.BootstrapRepositoryRequest) (bool, error) {
	for _, expected := range request.Files {
		var file struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		path := "/projects/" + strconv.FormatInt(request.ProviderProjectID, 10) + "/repository/files/" + url.PathEscape(expected.Path) + "?ref=" + url.QueryEscape(request.Branch)
		if err := c.do(ctx, http.MethodGet, path, nil, &file); err != nil {
			return false, err
		}
		if file.Encoding != "base64" {
			return false, errors.New("gitlab repository file uses unexpected encoding")
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
		if err != nil {
			return false, err
		}
		if !bytes.Equal(decoded, expected.Content) {
			return false, nil
		}
	}
	return true, nil
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
