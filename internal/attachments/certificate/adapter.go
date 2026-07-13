package certificate

import (
	"context"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
)

type Result struct{ ID, State, FailureCode string }
type Client interface {
	Ensure(context.Context, string, string) (Result, error)
	Status(context.Context, string) (Result, error)
}
type Adapter struct{ Client Client }

var _ application.CertificateProvider = Adapter{}

func mapped(value Result) application.CertificateResult {
	status := application.CertificatePending
	switch strings.ToUpper(value.State) {
	case "READY", "ISSUED":
		status = application.CertificateReady
	case "FAILED", "ERROR":
		status = application.CertificateFailed
	}
	return application.CertificateResult{CertificateID: value.ID, Status: status, FailureCode: value.FailureCode}
}
func (a Adapter) Ensure(ctx context.Context, tenant, hostname string) (application.CertificateResult, error) {
	if a.Client == nil {
		return application.CertificateResult{}, domain.NewError(domain.CodeUnavailable, "certificate provider unavailable")
	}
	value, err := a.Client.Ensure(ctx, tenant, hostname)
	if err != nil {
		return application.CertificateResult{}, domain.Wrap(domain.CodeRetryable, "certificate ensure failed", err)
	}
	return mapped(value), nil
}
func (a Adapter) Status(ctx context.Context, id string) (application.CertificateResult, error) {
	if a.Client == nil {
		return application.CertificateResult{}, domain.NewError(domain.CodeUnavailable, "certificate provider unavailable")
	}
	value, err := a.Client.Status(ctx, id)
	if err != nil {
		return application.CertificateResult{}, domain.Wrap(domain.CodeRetryable, "certificate status failed", err)
	}
	return mapped(value), nil
}
