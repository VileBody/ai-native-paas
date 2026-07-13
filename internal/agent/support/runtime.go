package support

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type Clock struct{}

func (Clock) Now() time.Time { return time.Now().UTC() }

type IDs struct{ sequence atomic.Uint64 }

func (i *IDs) NewID(prefix string) string {
	var entropy [10]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		// The monotonic component preserves process-local uniqueness even if the
		// kernel CSPRNG is temporarily unavailable. Production health checks
		// should fail on a sustained entropy-source failure.
		return fmt.Sprintf("%s-%016x", sanitizePrefix(prefix), i.sequence.Add(1))
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:]))
	return fmt.Sprintf("%s-%s-%06x", sanitizePrefix(prefix), encoded, i.sequence.Add(1))
}
func sanitizePrefix(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "id"
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "id"
	}
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}
