package application

import (
	"fmt"
	"sync/atomic"
	"time"
)

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type SequentialIDs struct{ value atomic.Uint64 }

func (g *SequentialIDs) New(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, g.value.Add(1))
}
