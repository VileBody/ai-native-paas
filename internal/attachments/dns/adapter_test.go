package dns

import (
	"context"
	"reflect"
	"testing"
)

type resolver struct{ values []string }

func (r resolver) LookupTXT(context.Context, string) ([]string, error) { return r.values, nil }
func TestDNSAdapter_ReadTXTChallenge(t *testing.T) {
	values, err := (Adapter{Resolver: resolver{values: []string{"challenge"}}}).ReadTXT(context.Background(), "_paas.example.com")
	if err != nil || !reflect.DeepEqual(values, []string{"challenge"}) {
		t.Fatalf("values=%v err=%v", values, err)
	}
}
func TestDNSAdapter_HandlesMultipleTXTValues(t *testing.T) {
	values, err := (Adapter{Resolver: resolver{values: []string{"b", "a", "b"}}}).ReadTXT(context.Background(), "_paas.example.com")
	if err != nil || !reflect.DeepEqual(values, []string{"a", "b"}) {
		t.Fatalf("values=%v err=%v", values, err)
	}
}
