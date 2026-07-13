// Package redact removes registered secret values from structured audit data and chunked streams.
package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const Replacement = "[REDACTED]"

type Stream struct {
	patterns  [][]byte
	maxLength int
	pending   []byte
	closed    bool
}

func NewStream(secrets ...string) (*Stream, error) {
	patterns := normalizePatterns(secrets)
	if len(patterns) == 0 {
		return nil, errors.New("at least one non-empty redaction secret is required")
	}
	return &Stream{patterns: patterns, maxLength: len(patterns[0])}, nil
}

func (s *Stream) Write(chunk []byte) ([]byte, error) {
	if s == nil || s.closed {
		return nil, errors.New("redaction stream is closed")
	}
	data := append(append([]byte(nil), s.pending...), chunk...)
	boundary := len(data) - (s.maxLength - 1)
	if boundary < 0 {
		boundary = 0
	}
	for _, pattern := range s.patterns {
		for offset := 0; offset < len(data); {
			index := bytes.Index(data[offset:], pattern)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(pattern)
			if start < boundary && end > boundary {
				boundary = start
			}
			offset = start + 1
		}
	}
	emit := replaceAll(data[:boundary], s.patterns)
	s.pending = append(s.pending[:0], data[boundary:]...)
	return emit, nil
}

func (s *Stream) Close() ([]byte, error) {
	if s == nil || s.closed {
		return nil, errors.New("redaction stream is closed")
	}
	s.closed = true
	emit := replaceAll(s.pending, s.patterns)
	s.pending = nil
	return emit, nil
}

func replaceAll(value []byte, patterns [][]byte) []byte {
	result := append([]byte(nil), value...)
	for _, pattern := range patterns {
		result = bytes.ReplaceAll(result, pattern, []byte(Replacement))
	}
	return result
}

func normalizePatterns(secrets []string) [][]byte {
	seen := make(map[string]struct{}, len(secrets))
	patterns := make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		seen[secret] = struct{}{}
		patterns = append(patterns, []byte(secret))
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) == len(patterns[j]) {
			return bytes.Compare(patterns[i], patterns[j]) < 0
		}
		return len(patterns[i]) > len(patterns[j])
	})
	return patterns
}

func JSON(raw json.RawMessage, secrets ...string) (json.RawMessage, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, errors.New("audit metadata is invalid JSON")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	patterns := normalizePatterns(secrets)
	value = sanitize(value, patterns, false)
	encoded, err := json.Marshal(value)
	return encoded, err
}

func sanitize(value any, patterns [][]byte, sensitive bool) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			result[key] = sanitize(nested, patterns, sensitive || sensitiveKey(key))
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, nested := range typed {
			result[index] = sanitize(nested, patterns, sensitive)
		}
		return result
	case string:
		if sensitive {
			return Replacement
		}
		return string(replaceAll([]byte(typed), patterns))
	default:
		if sensitive && typed != nil {
			return Replacement
		}
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, marker := range []string{"secret", "password", "passwd", "token", "authorization", "credential", "private_key", "api_key"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
