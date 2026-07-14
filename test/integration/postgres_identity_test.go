//go:build postgres_integration

package integration_test

import (
	"context"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
)

func TestPostgres_OIDCSubjectRequiresExactlyOneActiveBetaMembership(t *testing.T) {
	db := openPostgres(t)
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS platform_identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	resolver, err := oidcverify.NewPostgresResolver(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Migrate(context.Background()); err != nil {
		t.Fatalf("idempotent identity migration: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO platform_identity.oidc_subjects(issuer,subject,user_id) VALUES('https://gitlab.com','subject-1','user-1');
		INSERT INTO platform_identity.tenant_memberships(tenant_id,user_id,role,state) VALUES('tenant-1','user-1','OWNER','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	membership, err := resolver.ResolveMembership(context.Background(), "https://gitlab.com", "subject-1")
	if err != nil || membership.TenantID != "tenant-1" || membership.UserID != "user-1" {
		t.Fatalf("membership=%#v err=%v", membership, err)
	}
	if _, err := db.Exec(`INSERT INTO platform_identity.tenant_memberships(tenant_id,user_id,role,state) VALUES('tenant-2','user-1','MEMBER','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveMembership(context.Background(), "https://gitlab.com", "subject-1"); err == nil {
		t.Fatal("ambiguous active tenant membership was accepted")
	}
	if _, err := resolver.ResolveMembership(context.Background(), "https://gitlab.com", "unknown"); err == nil {
		t.Fatal("unknown OIDC subject was accepted")
	}
}
