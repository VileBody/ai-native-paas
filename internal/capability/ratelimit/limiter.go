// Package ratelimit provides the concurrency-safe policy core for capability
// gateway request admission. Provider credentials and request payloads never
// enter this component.
package ratelimit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidScope = errors.New("invalid capability rate-limit scope")
	ErrRateLimited  = errors.New("capability rate limit exceeded")
	ErrCapacity     = errors.New("capability rate limiter capacity exhausted")
)

type Scope struct {
	TenantID  string
	ProjectID string
}

type LimitError struct {
	RetryAfter time.Duration
}

func (e *LimitError) Error() string { return ErrRateLimited.Error() }
func (e *LimitError) Unwrap() error { return ErrRateLimited }

type bucket struct {
	windowStartedAt time.Time
	used            int
}

type Limiter struct {
	mu               sync.Mutex
	limit            int
	window           time.Duration
	maxTrackedScopes int
	buckets          map[string]bucket
}

func New(limit int, window time.Duration, maxTrackedScopes int) (*Limiter, error) {
	if limit < 1 || window < time.Second || maxTrackedScopes < 1 {
		return nil, errors.New("invalid capability rate-limit policy")
	}
	return &Limiter{limit: limit, window: window, maxTrackedScopes: maxTrackedScopes, buckets: map[string]bucket{}}, nil
}

func (l *Limiter) Allow(scope Scope, now time.Time) error {
	key, err := scopeKey(scope)
	if err != nil || l == nil || now.IsZero() {
		return ErrInvalidScope
	}
	now = now.UTC()
	l.mu.Lock()
	defer l.mu.Unlock()

	current, found := l.buckets[key]
	if found && !now.Before(current.windowStartedAt) {
		if !now.Before(current.windowStartedAt.Add(l.window)) {
			current = bucket{windowStartedAt: now}
		}
	} else if found {
		// A clock moving backwards must not reset or extend admission. Keep the
		// established window and fail closed once its capacity is exhausted.
		now = current.windowStartedAt
	}
	if !found {
		if len(l.buckets) >= l.maxTrackedScopes {
			l.pruneExpired(now)
		}
		if len(l.buckets) >= l.maxTrackedScopes {
			return ErrCapacity
		}
		current = bucket{windowStartedAt: now}
	}
	if current.used >= l.limit {
		retryAfter := current.windowStartedAt.Add(l.window).Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		l.buckets[key] = current
		return &LimitError{RetryAfter: retryAfter}
	}
	current.used++
	l.buckets[key] = current
	return nil
}

func (l *Limiter) pruneExpired(now time.Time) {
	for key, value := range l.buckets {
		if !now.Before(value.windowStartedAt.Add(l.window)) {
			delete(l.buckets, key)
		}
	}
}

func scopeKey(scope Scope) (string, error) {
	tenantID := strings.TrimSpace(scope.TenantID)
	projectID := strings.TrimSpace(scope.ProjectID)
	if tenantID == "" || projectID == "" || len(tenantID) > 255 || len(projectID) > 255 || strings.ContainsAny(tenantID+projectID, "\x00\r\n\t") {
		return "", ErrInvalidScope
	}
	// Length-prefix both dimensions so no pair can alias another scope.
	return fmt.Sprintf("%d:%s%d:%s", len(tenantID), tenantID, len(projectID), projectID), nil
}
