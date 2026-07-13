package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	New(prefix string) string
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type CryptoIDGenerator struct{}

func (CryptoIDGenerator) New(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

// FixedClock is exported from the internal package for deterministic tests and
// local development harnesses.
type FixedClock struct {
	mu sync.RWMutex
	t  time.Time
}

func NewFixedClock(t time.Time) *FixedClock { return &FixedClock{t: t.UTC()} }

func (c *FixedClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.t
}

func (c *FixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type SequenceIDGenerator struct {
	mu   sync.Mutex
	next int64
}

func NewSequenceIDGenerator() *SequenceIDGenerator { return &SequenceIDGenerator{} }

func (g *SequenceIDGenerator) New(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return fmt.Sprintf("%s_%06d", prefix, g.next)
}
