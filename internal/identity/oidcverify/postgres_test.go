package oidcverify

import (
	"context"
	"strings"
	"testing"
)

func TestNewPostgresVerifierRejectsIncompleteProductionConfiguration(t *testing.T) {
	for _, test := range []struct {
		name     string
		issuer   string
		clientID string
		want     string
	}{
		{name: "missing issuer", clientID: "client-1", want: "issuer"},
		{name: "missing client", issuer: "https://issuer.example", want: "client id"},
		{name: "missing database", issuer: "https://issuer.example", clientID: "client-1", want: "postgres db"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPostgresVerifier(context.Background(), nil, test.issuer, test.clientID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}
