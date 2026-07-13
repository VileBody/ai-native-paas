package webhook

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type Normalizer struct{ Provider string }
type gitLabEvent struct {
	ObjectKind     string `json:"object_kind"`
	EventName      string `json:"event_name"`
	Before         string `json:"before"`
	After          string `json:"after"`
	CheckoutSHA    string `json:"checkout_sha"`
	Ref            string `json:"ref"`
	EventCreatedAt string `json:"event_created_at"`
	Project        struct {
		ID int64 `json:"id"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int64  `json:"iid"`
		Action       string `json:"action"`
		State        string `json:"state"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		UpdatedAt string `json:"updated_at"`
	} `json:"object_attributes"`
}

func (n Normalizer) Normalize(eventID string, raw []byte, now time.Time) (application.NormalizedWebhook, error) {
	var e gitLabEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return application.NormalizedWebhook{}, err
	}
	provider := n.Provider
	if provider == "" {
		provider = "gitlab"
	}
	kind := strings.ToLower(strings.TrimSpace(e.ObjectKind))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(e.EventName))
	}
	occurred := parseTime(e.EventCreatedAt, now)
	switch kind {
	case "push", "push hook":
		branch := strings.TrimPrefix(e.Ref, "refs/heads/")
		if e.Project.ID <= 0 || branch == "" || e.After == "" {
			return application.NormalizedWebhook{}, errors.New("invalid push payload")
		}
		p := &application.PushEvent{EventID: eventID, Provider: provider, ProviderProjectID: e.Project.ID, Branch: branch, BeforeSHA: e.Before, AfterSHA: e.After, OccurredAt: occurred}
		return application.NormalizedWebhook{Push: p, SourceEvent: sourcev1.SourceEvent{EventID: eventID, Kind: sourcev1.EventPush, Provider: provider, ProviderProjectID: e.Project.ID, Branch: branch, BeforeSHA: e.Before, AfterSHA: e.After, OccurredAt: occurred}}, nil
	case "merge_request", "merge request hook":
		a := e.ObjectAttributes
		at := parseTime(a.UpdatedAt, occurred)
		if e.Project.ID <= 0 || a.IID <= 0 {
			return application.NormalizedWebhook{}, errors.New("invalid merge request payload")
		}
		mr := &application.MergeRequestEvent{EventID: eventID, Provider: provider, ProviderProjectID: e.Project.ID, IID: a.IID, Action: a.Action, State: a.State, SourceBranch: a.SourceBranch, TargetBranch: a.TargetBranch, HeadSHA: a.LastCommit.ID, OccurredAt: at}
		return application.NormalizedWebhook{MergeRequest: mr, SourceEvent: sourcev1.SourceEvent{EventID: eventID, Kind: sourcev1.EventMergeRequest, Provider: provider, ProviderProjectID: e.Project.ID, MergeRequestIID: a.IID, Action: a.Action, AfterSHA: a.LastCommit.ID, OccurredAt: at}}, nil
	default:
		return application.NormalizedWebhook{}, errors.New("unsupported gitlab webhook event")
	}
}
func parseTime(v string, fallback time.Time) time.Time {
	if v == "" {
		return fallback.UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05 UTC"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return fallback.UTC()
}
