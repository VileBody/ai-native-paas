package workspaceagent

import (
	"bytes"
	"io"
	"sort"
	"sync"
)

var redactionMarker = []byte("[REDACTED]")

type boundedSink struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	budget *outputBudget
}

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
	truncated bool
}

func newBoundedSinks(limit int64) (*boundedSink, *boundedSink) {
	budget := &outputBudget{remaining: limit}
	return &boundedSink{budget: budget}, &boundedSink{budget: budget}
}

func (s *boundedSink) Write(raw []byte) (int, error) {
	wanted := len(raw)
	s.budget.mu.Lock()
	allowed := int64(len(raw))
	if allowed > s.budget.remaining {
		allowed = s.budget.remaining
		s.budget.truncated = true
	}
	s.budget.remaining -= allowed
	s.budget.mu.Unlock()
	raw = raw[:int(allowed)]
	if len(raw) > 0 {
		s.mu.Lock()
		_, _ = s.buffer.Write(raw)
		s.mu.Unlock()
	}
	return wanted, nil
}

func (s *boundedSink) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buffer.Bytes()...)
}

func (s *boundedSink) Truncated() bool {
	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	return s.budget.truncated
}

type redactingWriter struct {
	mu          sync.Mutex
	destination io.Writer
	secrets     [][]byte
	maximum     int
	pending     []byte
	escape      bool
	csi         bool
}

func newRedactingWriter(destination io.Writer, values []string) *redactingWriter {
	unique := make(map[string]struct{})
	for _, value := range values {
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	secrets := make([][]byte, 0, len(unique))
	maximum := 1
	for value := range unique {
		secrets = append(secrets, []byte(value))
		if len(value) > maximum {
			maximum = len(value)
		}
	}
	sort.Slice(secrets, func(left, right int) bool { return len(secrets[left]) > len(secrets[right]) })
	return &redactingWriter{destination: destination, secrets: secrets, maximum: maximum}
}

func (w *redactingWriter) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	normalized := w.stripANSI(raw)
	w.pending = append(w.pending, normalized...)
	w.pending = w.redact(w.pending)
	retain := w.maximum - 1
	if len(w.pending) <= retain {
		return len(raw), nil
	}
	emit := len(w.pending) - retain
	if _, err := w.destination.Write(w.pending[:emit]); err != nil {
		return 0, err
	}
	w.pending = append(w.pending[:0], w.pending[emit:]...)
	return len(raw), nil
}

func (w *redactingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = w.redact(w.pending)
	if len(w.pending) == 0 {
		return nil
	}
	_, err := w.destination.Write(w.pending)
	clearBytes(w.pending)
	w.pending = nil
	return err
}

func (w *redactingWriter) redact(raw []byte) []byte {
	for _, secret := range w.secrets {
		raw = bytes.ReplaceAll(raw, secret, redactionMarker)
	}
	return raw
}

// stripANSI removes CSI escape sequences before matching, so a process cannot
// hide a secret by placing terminal color codes between sentinel fragments.
func (w *redactingWriter) stripANSI(raw []byte) []byte {
	result := make([]byte, 0, len(raw))
	for _, value := range raw {
		if w.csi {
			if value >= 0x40 && value <= 0x7e {
				w.csi = false
			}
			continue
		}
		if w.escape {
			w.escape = false
			if value == '[' {
				w.csi = true
			}
			continue
		}
		if value == 0x1b {
			w.escape = true
			continue
		}
		result = append(result, value)
	}
	return result
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
