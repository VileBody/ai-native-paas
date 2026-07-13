package dns

import (
	"context"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
)

type Resolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}
type Adapter struct{ Resolver Resolver }

var _ application.DNSResolver = Adapter{}

func (a Adapter) ReadTXT(ctx context.Context, name string) ([]string, error) {
	hostname, err := canonicalLookupName(name)
	if err != nil {
		return nil, err
	}
	if a.Resolver == nil {
		return nil, domain.NewError(domain.CodeUnavailable, "DNS resolver unavailable")
	}
	values, err := a.Resolver.LookupTXT(ctx, hostname)
	if err != nil {
		return nil, domain.Wrap(domain.CodeRetryable, "DNS lookup failed", err)
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out, nil
}

// canonicalLookupName accepts DNS owner names such as _paas.example.com.
// Domain.CanonicalHostname intentionally rejects underscores because they are
// invalid in user-facing hostnames, but underscores are conventional for TXT
// verification records.
func canonicalLookupName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || len(name) > 253 {
		return "", domain.NewError(domain.CodeInvalidArgument, "invalid DNS lookup name")
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", domain.NewError(domain.CodeInvalidArgument, "invalid DNS lookup name")
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
				return "", domain.NewError(domain.CodeInvalidArgument, "invalid DNS lookup name")
			}
		}
	}
	return name, nil
}
