package domain_test

import (
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

var now = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

func TestProject_CreateNormalizesSlug(t *testing.T) {
	p, err := domain.NewProject("p1", "t1", "  Booking API  ", now)
	if err != nil || p.Slug != "booking-api" {
		t.Fatalf("project=%+v err=%v", p, err)
	}
}
func TestProject_CreateRejectsEmptyIdentity(t *testing.T) {
	if _, err := domain.NewProject("", "t", "x", now); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
func TestProject_RenameAdvancesVersion(t *testing.T) {
	p, _ := domain.NewProject("p", "t", "one", now)
	if err := p.Rename("Two", now.Add(time.Minute)); err != nil || p.Version != 2 || p.Slug != "two" {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestRepository_ProvisioningLifecycle(t *testing.T) {
	r, _ := domain.NewRepository("r", "t", "p", "gitlab", "corr", 7, now)
	if err := r.BeginProvisioning(now); err != nil {
		t.Fatal(err)
	}
	if err := r.AttachProvider(42, "g/p", "https://g/p", "main", now); err != nil {
		t.Fatal(err)
	}
	if r.State != domain.RepositoryReady || r.ProviderProjectID != 42 {
		t.Fatalf("%+v", r)
	}
}
func TestRepository_ProviderIdentityIsImmutable(t *testing.T) {
	r, _ := domain.NewRepository("r", "t", "p", "gitlab", "corr", 7, now)
	_ = r.BeginProvisioning(now)
	_ = r.AttachProvider(42, "g/p", "", "main", now)
	if err := r.AttachProvider(43, "g/p", "", "main", now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestRepository_MetadataRenameKeepsProviderID(t *testing.T) {
	r, _ := domain.NewRepository("r", "t", "p", "gitlab", "corr", 7, now)
	_ = r.BeginProvisioning(now)
	_ = r.AttachProvider(42, "g/p", "", "main", now)
	if err := r.SyncProviderMetadata(42, "g/new", "https://g/new", "main", now); err != nil || r.ProviderProjectID != 42 || r.ProviderPath != "g/new" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestBranchPush_DuplicateIsNoop(t *testing.T) {
	b, _ := domain.NewBranchHead("r", "main")
	changed, err := b.ApplyPush("", sha("a"), "e1", now, now)
	if !changed || err != nil {
		t.Fatal(changed, err)
	}
	v := b.Version
	changed, err = b.ApplyPush("", sha("a"), "e1", now, now)
	if changed || err != nil || b.Version != v {
		t.Fatal(changed, err, b.Version)
	}
}
func TestBranchPush_OutOfOrderIsRejected(t *testing.T) {
	b, _ := domain.NewBranchHead("r", "main")
	_, _ = b.ApplyPush("", sha("a"), "e2", now.Add(time.Minute), now)
	if _, err := b.ApplyPush(sha("a"), sha("b"), "e1", now, now); !domain.HasCode(err, domain.CodeStaleVersion) {
		t.Fatalf("err=%v", err)
	}
}
func TestBranchPush_GapRequiresAuthoritativeReconcile(t *testing.T) {
	b, _ := domain.NewBranchHead("r", "main")
	_, _ = b.ApplyPush("", sha("a"), "e1", now, now)
	if _, err := b.ApplyPush(sha("x"), sha("b"), "e2", now.Add(time.Minute), now); !domain.HasCode(err, domain.CodeStaleVersion) {
		t.Fatalf("err=%v", err)
	}
}
func TestBranch_AuthoritativeHeadCanMoveAfterForcePush(t *testing.T) {
	b, _ := domain.NewBranchHead("r", "main")
	_, _ = b.SetAuthoritative(sha("a"), now)
	changed, err := b.SetAuthoritative(sha("f"), now.Add(time.Minute))
	if !changed || err != nil || b.CommitSHA != sha("f") {
		t.Fatal(changed, err, b)
	}
}
func TestMergeRequest_Lifecycle(t *testing.T) {
	m := domain.MergeRequest{RepositoryID: "r", ProviderIID: 1, Version: 1}
	if err := m.Apply("open", "opened", sha("a"), now); err != nil || m.State != domain.MergeRequestOpen {
		t.Fatal(m, err)
	}
	if err := m.Apply("merge", "merged", sha("b"), now); err != nil || m.State != domain.MergeRequestMerged {
		t.Fatal(m, err)
	}
}
func TestWorkspace_ValidLifecycle(t *testing.T) {
	w, err := domain.NewWorkspace("w", "t", "r", "feature", sha("a"), now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_ = w.MarkCloned("/tmp/w", "c", now)
	_ = w.MarkPatched(now)
	_ = w.MarkCommitted(sha("b"), now)
	_ = w.MarkPushed(now)
	_ = w.MarkCompleted(now)
	if w.State != domain.WorkspaceCompleted || w.CredentialID != "" {
		t.Fatalf("%+v", w)
	}
}
func TestWorkspace_RejectsBackwardTransition(t *testing.T) {
	w, _ := domain.NewWorkspace("w", "t", "r", "feature", sha("a"), now, now.Add(time.Hour))
	if err := w.MarkPushed(now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestWorkspace_ExpiredMayBeDeleted(t *testing.T) {
	w, _ := domain.NewWorkspace("w", "t", "r", "feature", sha("a"), now, now.Add(time.Minute))
	if err := w.MarkExpired(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.MarkDeleted(now.Add(time.Hour)); err != nil || w.State != domain.WorkspaceDeleted {
		t.Fatal(err, w.State)
	}
}
func sha(c string) string {
	v := ""
	for len(v) < 40 {
		v += c
	}
	return v[:40]
}
