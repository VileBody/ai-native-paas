package support

import (
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"
)

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type IDs struct{ counter atomic.Uint64 }

func (i *IDs) NewID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := i.counter.Add(1)
	return prefix + "_" + hex.EncodeToString(b[:]) + "_" + base36(n)
}
func base36(v uint64) string {
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	if v == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = chars[v%36]
		v /= 36
	}
	return string(b[i:])
}
