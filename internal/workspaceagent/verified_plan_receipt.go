package workspaceagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type receiptPlan struct {
	ResourceChanges []struct {
		Address      string `json:"address"`
		ProviderName string `json:"provider_name"`
		Type         string `json:"type"`
		Change       struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

func normalizePlanReceipt(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 32<<20 {
		return nil, errors.New("OpenTofu plan JSON exceeds receipt limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var input receiptPlan
	if err := decoder.Decode(&input); err != nil {
		return nil, errors.New("decode OpenTofu plan receipt")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode OpenTofu plan receipt")
	}
	for _, change := range input.ResourceChanges {
		if strings.TrimSpace(change.Address) == "" || strings.TrimSpace(change.Type) == "" || len(change.Change.Actions) == 0 {
			return nil, errors.New("OpenTofu plan receipt contains an invalid change")
		}
	}
	normalized, err := json.Marshal(input)
	if err != nil || len(normalized) > 8<<20 {
		return nil, errors.New("normalized OpenTofu plan receipt exceeds limit")
	}
	return normalized, nil
}
