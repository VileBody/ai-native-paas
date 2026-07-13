package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func dnsSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	previousDash := false
	for _, r := range value {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			previousDash = false
			continue
		}
		if !previousDash && b.Len() > 0 {
			b.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortHash(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:5])
}

func boundedDNS(prefix string, values ...string) string {
	slug := dnsSlug(strings.Join(values, "-"))
	if slug == "" {
		slug = "app"
	}
	suffix := "-" + shortHash(values...)
	max := 63 - len(prefix) - len(suffix)
	if max < 1 {
		max = 1
	}
	if len(slug) > max {
		slug = strings.Trim(slug[:max], "-")
	}
	if slug == "" {
		slug = "app"
	}
	return prefix + slug + suffix
}

func EnvironmentNamespace(applicationID, environment string) string {
	return boundedDNS("app-", applicationID, environment)
}

func PaaSAppName(applicationID, environment string) string {
	return boundedDNS("paas-", applicationID, environment)
}

func GeneratedHostname(applicationID, environment, ingressDomain string) (string, error) {
	ingressDomain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(ingressDomain, ".")))
	if !runtimev1.ValidDNSSubdomain(ingressDomain) {
		return "", NewError(CodeInvalidArgument, "runtime cell ingress domain is invalid")
	}
	label := boundedDNS("", applicationID, environment)
	hostname := label + "." + ingressDomain
	if !runtimev1.ValidDNSSubdomain(hostname) {
		return "", NewError(CodeInvalidArgument, "generated hostname is invalid")
	}
	return hostname, nil
}
