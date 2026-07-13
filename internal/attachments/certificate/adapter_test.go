package certificate

import (
	"context"
	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"testing"
)

type client struct{ result Result }

func (c client) Ensure(context.Context, string, string) (Result, error) { return c.result, nil }
func (c client) Status(context.Context, string) (Result, error)         { return c.result, nil }
func TestCertificateAdapter_MapsPendingReadyFailed(t *testing.T) {
	cases := map[string]application.CertificateStatus{"PENDING": application.CertificatePending, "ISSUED": application.CertificateReady, "FAILED": application.CertificateFailed}
	for input, expected := range cases {
		value, err := (Adapter{Client: client{result: Result{ID: "cert", State: input}}}).Status(context.Background(), "cert")
		if err != nil || value.Status != expected {
			t.Fatalf("input=%s value=%+v err=%v", input, value, err)
		}
	}
}
