package workspaceagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
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

func retainedResourcesFromContract(contract projectv2.Contract) []infrastructurev1.RetainedResource {
	result := make([]infrastructurev1.RetainedResource, 0, len(contract.Infrastructure.Retention))
	for _, rule := range contract.Infrastructure.Retention {
		result = append(result, infrastructurev1.RetainedResource{
			Address: strings.TrimSpace(rule.Address), Provider: strings.TrimSpace(rule.Provider), ResourceType: strings.TrimSpace(rule.ResourceType),
			ExternalID: strings.TrimSpace(rule.ExternalID), Policy: "platform.yaml/v2:" + strings.TrimSpace(rule.Policy), Reason: strings.TrimSpace(rule.Reason),
		})
	}
	return result
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
