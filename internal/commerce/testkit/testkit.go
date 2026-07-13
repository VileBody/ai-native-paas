package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Clock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.T }
func (c *Clock) Set(t time.Time)         { c.mu.Lock(); c.T = t; c.mu.Unlock() }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.T = c.T.Add(d); c.mu.Unlock() }

type IDs struct {
	mu sync.Mutex
	n  int64
}

func (i *IDs) NewID(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%04d", prefix, i.n)
}

type Ownership struct {
	mu      sync.Mutex
	Allowed map[string]bool
	Err     error
}

func (o *Ownership) Owns(_ context.Context, tenantID, resourceType, resourceID string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Err != nil {
		return false, o.Err
	}
	return o.Allowed[tenantID+"/"+resourceType+"/"+resourceID], nil
}
