package support

import (
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"
)

type Clock struct{}

func (Clock) Now() time.Time { return time.Now().UTC() }

type IDs struct{ counter atomic.Uint64 }

func (i *IDs) NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(b[:])
	}
	return prefix + "_" + itoa(i.counter.Add(1))
}
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for v > 0 {
		n--
		b[n] = byte('0' + v%10)
		v /= 10
	}
	return string(b[n:])
}
