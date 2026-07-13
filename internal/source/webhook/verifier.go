package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Verifier struct {
	Secret       []byte
	ReplayWindow time.Duration
	AllowLegacy  bool
	LegacyToken  string
}

func (v Verifier) Verify(headers map[string][]string, body []byte, now time.Time) (string, error) {
	id := first(headers, "webhook-id", "x-gitlab-event-uuid", "x-gitlab-webhook-uuid")
	ts := first(headers, "webhook-timestamp", "x-gitlab-webhook-timestamp")
	sig := first(headers, "webhook-signature", "x-gitlab-webhook-signature")
	if id != "" && ts != "" && sig != "" {
		if len(v.Secret) == 0 {
			return "", errors.New("webhook signing secret is not configured")
		}
		seconds, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return "", errors.New("invalid webhook timestamp")
		}
		at := time.Unix(seconds, 0)
		window := v.ReplayWindow
		if window <= 0 {
			window = 5 * time.Minute
		}
		delta := now.Sub(at)
		if delta < 0 {
			delta = -delta
		}
		if delta > window {
			return "", errors.New("webhook timestamp outside replay window")
		}
		mac := hmac.New(sha256.New, v.Secret)
		_, _ = mac.Write([]byte(id + "." + ts + "."))
		_, _ = mac.Write(body)
		expected := mac.Sum(nil)
		if !matchSignature(sig, expected) {
			return "", errors.New("invalid webhook signature")
		}
		return id, nil
	}
	if v.AllowLegacy {
		token := first(headers, "x-gitlab-token")
		if token == "" || v.LegacyToken == "" || !hmac.Equal([]byte(token), []byte(v.LegacyToken)) {
			return "", errors.New("invalid legacy webhook token")
		}
		if id == "" {
			sum := sha256.Sum256(body)
			id = "legacy-" + hex.EncodeToString(sum[:16])
		}
		return id, nil
	}
	return "", errors.New("signed webhook headers required")
}
func matchSignature(raw string, expected []byte) bool {
	for _, part := range strings.Fields(raw) {
		part = strings.Trim(part, " ,")
		var candidate []byte
		var err error
		switch {
		case strings.HasPrefix(part, "v1,"):
			candidate, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(part, "v1,"))
		case strings.HasPrefix(part, "v1="):
			candidate, err = hex.DecodeString(strings.TrimPrefix(part, "v1="))
		case strings.HasPrefix(part, "sha256="):
			candidate, err = hex.DecodeString(strings.TrimPrefix(part, "sha256="))
		default:
			continue
		}
		if err == nil && hmac.Equal(candidate, expected) {
			return true
		}
	}
	return false
}
func first(headers map[string][]string, names ...string) string {
	for _, name := range names {
		for k, vs := range headers {
			if strings.EqualFold(k, name) && len(vs) > 0 {
				return strings.TrimSpace(vs[0])
			}
		}
	}
	return ""
}
func Sign(secret []byte, id string, at time.Time, body []byte) (timestamp, signature string) {
	timestamp = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = fmt.Fprintf(mac, "%s.%s.", id, timestamp)
	_, _ = mac.Write(body)
	return timestamp, "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
