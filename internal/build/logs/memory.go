package logs

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/build/application"
)

type Memory struct {
	mu      sync.Mutex
	buffers map[string]*bytes.Buffer
	pending map[string]string
	secrets map[string][]string
}

func New() *Memory {
	return &Memory{buffers: map[string]*bytes.Buffer{}, pending: map[string]string{}, secrets: map[string][]string{}}
}
func (m *Memory) Writer(buildID string, secrets []string) io.Writer {
	m.mu.Lock()
	defer m.mu.Unlock()
	buffer := m.buffers[buildID]
	if buffer == nil {
		buffer = &bytes.Buffer{}
		m.buffers[buildID] = buffer
	}
	m.secrets[buildID] = mergeSecrets(m.secrets[buildID], cleanSecrets(secrets))
	return &redactingWriter{parent: m, buildID: buildID}
}
func (m *Memory) Read(ctx context.Context, buildID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buffers[buildID] == nil {
		return nil, nil
	}
	out := append([]byte(nil), m.buffers[buildID].Bytes()...)
	// pending contains a suffix that could be the beginning of a secret. Never
	// expose it through a concurrent log read; a later write may complete the
	// secret across an arbitrary io.Writer chunk boundary.
	if m.pending[buildID] != "" {
		out = append(out, []byte("[REDACTED]")...)
	}
	return out, nil
}

type redactingWriter struct {
	parent  *Memory
	buildID string
}

func (w *redactingWriter) Write(raw []byte) (int, error) {
	w.parent.mu.Lock()
	defer w.parent.mu.Unlock()
	buffer := w.parent.buffers[w.buildID]
	if buffer == nil {
		buffer = &bytes.Buffer{}
		w.parent.buffers[w.buildID] = buffer
	}
	combined := w.parent.pending[w.buildID] + string(raw)
	safe, pending := redactChunk(combined, w.parent.secrets[w.buildID])
	w.parent.pending[w.buildID] = pending
	_, err := buffer.WriteString(safe)
	return len(raw), err
}
func cleanSecrets(in []string) []string {
	var out []string
	for _, value := range in {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

var _ application.LogStore = (*Memory)(nil)

func mergeSecrets(existing, added []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(added))
	out := make([]string, 0, len(existing)+len(added))
	for _, values := range [][]string{existing, added} {
		for _, value := range values {
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	// Replace longer secrets first so a short token cannot expose the suffix of
	// a longer token that contains it.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func redactChunk(text string, secrets []string) (safe, pending string) {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	longest := 0
	for _, secret := range secrets {
		limit := len(secret) - 1
		if len(text) < limit {
			limit = len(text)
		}
		for size := 1; size <= limit; size++ {
			if size > longest && strings.HasSuffix(text, secret[:size]) {
				longest = size
			}
		}
	}
	if longest == 0 {
		return text, ""
	}
	return text[:len(text)-longest], text[len(text)-longest:]
}
