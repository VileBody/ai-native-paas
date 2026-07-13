package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
	"github.com/keir-research/ai-native-paas/internal/kernel/memory"
)

var fixedTime = time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC)

type harness struct {
	service *kernel.Service
	store   *memory.Store
	clock   *kernel.FixedClock
	ids     *kernel.SequenceIDGenerator
}

func newHarness(t *testing.T) harness {
	t.Helper()
	store := memory.NewStore()
	clock := kernel.NewFixedClock(fixedTime)
	ids := kernel.NewSequenceIDGenerator()
	service, err := kernel.NewService(store, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	return harness{service: service, store: store, clock: clock, ids: ids}
}

func user(id string, scopes ...string) kernelv1.PrincipalContext {
	return kernelv1.PrincipalContext{PrincipalID: kernelv1.PrincipalID(id), Kind: kernelv1.PrincipalKindUser, Scopes: scopes}
}

func meta(principal kernelv1.PrincipalContext, key string) kernelv1.CommandMeta {
	return kernelv1.CommandMeta{
		Principal:      principal,
		CorrelationID:  kernelv1.CorrelationID("cor_" + key),
		IdempotencyKey: kernelv1.IdempotencyKey(key),
	}
}

func createOrganization(t *testing.T, h harness, ownerID, key, name string) kernel.CreateOrganizationResult {
	t.Helper()
	result, err := h.service.CreateOrganization(context.Background(), kernel.CreateOrganizationCommand{
		Meta: meta(user(ownerID, "kernel:*"), key),
		Name: name,
	})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	return result
}

func TestIdempotency_SameKeyAndPayloadReturnsOriginalResult(t *testing.T) {
	h := newHarness(t)
	command := kernel.CreateOrganizationCommand{Meta: meta(user("owner", "kernel:*"), "same"), Name: "Acme"}
	first, err := h.service.CreateOrganization(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.CreateOrganization(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replayed result differs:\nfirst=%+v\nsecond=%+v", first, second)
	}
	outbox := readOutbox(t, h.store)
	if got, want := len(outbox), 5; got != want {
		t.Fatalf("outbox events = %d, want %d (replay must not duplicate side effects)", got, want)
	}
}

func TestIdempotency_SameKeyDifferentPayloadReturnsConflict(t *testing.T) {
	h := newHarness(t)
	principal := user("owner", "kernel:*")
	_, err := h.service.CreateOrganization(context.Background(), kernel.CreateOrganizationCommand{Meta: meta(principal, "same"), Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.CreateOrganization(context.Background(), kernel.CreateOrganizationCommand{Meta: meta(principal, "same"), Name: "Beta"})
	if kernel.ErrorCode(err) != kernelv1.CodeIdempotencyConflict {
		t.Fatalf("error = %v, want IDEMPOTENCY_CONFLICT", err)
	}
}

func TestIdempotency_ConcurrentSameKeyCreatesOneOrganization(t *testing.T) {
	h := newHarness(t)
	const workers = 64
	command := kernel.CreateOrganizationCommand{Meta: meta(user("owner", "kernel:*"), "concurrent"), Name: "Acme"}
	results := make(chan kernel.CreateOrganizationResult, workers)
	failures := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			result, err := h.service.CreateOrganization(context.Background(), command)
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Errorf("concurrent command failed: %v", err)
	}
	uniqueOrganizations := map[kernelv1.TenantID]struct{}{}
	uniqueOperations := map[kernelv1.OperationID]struct{}{}
	for result := range results {
		uniqueOrganizations[result.Organization.OrganizationID] = struct{}{}
		uniqueOperations[result.Operation.OperationID] = struct{}{}
	}
	if len(uniqueOrganizations) != 1 || len(uniqueOperations) != 1 {
		t.Fatalf("unique organizations=%d operations=%d, want one each", len(uniqueOrganizations), len(uniqueOperations))
	}
	outbox := readOutbox(t, h.store)
	if len(outbox) != 5 {
		t.Fatalf("outbox events = %d, want 5", len(outbox))
	}
}

func TestIdempotency_RetryAfterTransportFailureReturnsExistingOperation(t *testing.T) {
	h := newHarness(t)
	command := kernel.CreateOrganizationCommand{Meta: meta(user("owner", "kernel:*"), "transport"), Name: "Acme"}
	first, err := h.service.CreateOrganization(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a lost HTTP response: the client did not observe first, but the
	// committed transaction remains. A retry must return the same operation.
	second, err := h.service.CreateOrganization(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if second.Operation.OperationID != first.Operation.OperationID {
		t.Fatalf("operation changed after retry: %s -> %s", first.Operation.OperationID, second.Operation.OperationID)
	}
}

func TestAuthorization_ServiceDeniesCrossTenantContext(t *testing.T) {
	h := newHarness(t)
	first := createOrganization(t, h, "owner", "org-a", "Alpha")
	second := createOrganization(t, h, "owner", "org-b", "Beta")
	principal := user("owner", "kernel:*").WithTenant(first.Organization.OrganizationID)
	_, err := h.service.GetOrganization(context.Background(), principal, second.Organization.OrganizationID, "cor_cross")
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("cross-tenant read error = %v, want FORBIDDEN", err)
	}
}

func TestMembership_AdminCannotPromoteMemberToOwner(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create", "Acme")
	orgID := created.Organization.OrganizationID
	owner := user("owner", "kernel:*")
	_, err := h.service.InviteMember(context.Background(), kernel.InviteMemberCommand{
		Meta: meta(owner, "invite-admin"), OrganizationID: orgID, PrincipalID: "admin", Role: kernel.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.AcceptInvitation(context.Background(), kernel.AcceptInvitationCommand{
		Meta: meta(user("admin", "kernel:*"), "accept-admin"), OrganizationID: orgID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.InviteMember(context.Background(), kernel.InviteMemberCommand{
		Meta: meta(owner, "invite-dev"), OrganizationID: orgID, PrincipalID: "developer", Role: kernel.RoleDeveloper,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.ChangeMemberRole(context.Background(), kernel.ChangeMemberRoleCommand{
		Meta: meta(user("admin", "kernel:*"), "promote"), OrganizationID: orgID, PrincipalID: "developer", Role: kernel.RoleOwner,
	})
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("admin promotion error = %v, want FORBIDDEN", err)
	}
}

func TestOperationService_StartTransitionGet(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create", "Acme")
	operation, err := h.service.Start(context.Background(), kernelv1.CommandMeta{
		TenantID:       created.Organization.OrganizationID,
		Principal:      user("owner", "kernel:*"),
		CorrelationID:  "cor_manual",
		IdempotencyKey: "manual-op",
		Command:        "ManualOperation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != kernelv1.OperationPending {
		t.Fatalf("state = %s, want PENDING", operation.State)
	}
	if err := h.service.Transition(context.Background(), operation.OperationID, kernelv1.OperationRunning, kernelv1.OperationResult{Code: "RUNNING"}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Transition(context.Background(), operation.OperationID, kernelv1.OperationSucceeded, kernelv1.OperationResult{Code: "SUCCEEDED", Data: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.service.Get(context.Background(), operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != kernelv1.OperationSucceeded || snapshot.Version != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestOutbox_AggregateAndEventCommitAtomically(t *testing.T) {
	store := memory.NewStore()
	organization, _ := kernel.NewOrganization("org_1", "Acme", "owner", fixedTime)
	event := sampleEvent("evt_1", map[string]any{"organization_id": "org_1"})
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(context.Background(), organization); err != nil {
			return err
		}
		return tx.AppendOutbox(context.Background(), kernel.OutboxRecord{Event: event, State: kernel.OutboxPending, CreatedAt: fixedTime})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(context.Background(), func(reader kernel.Reader) error {
		if _, err := reader.GetOrganization(context.Background(), "org_1"); err != nil {
			return err
		}
		records, err := reader.ListOutbox(context.Background())
		if err != nil {
			return err
		}
		if len(records) != 1 || records[0].Event.EventID != "evt_1" {
			t.Fatalf("outbox records = %+v", records)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOutbox_RollbackLeavesNeitherAggregateNorEvent(t *testing.T) {
	store := memory.NewStore()
	organization, _ := kernel.NewOrganization("org_1", "Acme", "owner", fixedTime)
	sentinel := errors.New("force rollback")
	err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(context.Background(), organization); err != nil {
			return err
		}
		if err := tx.AppendOutbox(context.Background(), kernel.OutboxRecord{Event: sampleEvent("evt_1", nil), State: kernel.OutboxPending, CreatedAt: fixedTime}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("transaction error = %v", err)
	}
	if err := store.View(context.Background(), func(reader kernel.Reader) error {
		if _, err := reader.GetOrganization(context.Background(), "org_1"); kernel.ErrorCode(err) != kernelv1.CodeNotFound {
			t.Fatalf("organization survived rollback: %v", err)
		}
		records, err := reader.ListOutbox(context.Background())
		if err != nil {
			return err
		}
		if len(records) != 0 {
			t.Fatalf("outbox survived rollback: %+v", records)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type publisherSpy struct {
	mu       sync.Mutex
	events   []kernelv1.DomainEventEnvelope[json.RawMessage]
	failures int
}

func (p *publisherSpy) Publish(_ context.Context, event kernelv1.DomainEventEnvelope[json.RawMessage]) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures > 0 {
		p.failures--
		return errors.New("broker unavailable")
	}
	p.events = append(p.events, event)
	return nil
}

func TestOutbox_DispatchMarksEventAfterSuccessfulPublish(t *testing.T) {
	store := memory.NewStore()
	appendEvent(t, store, sampleEvent("evt_1", nil))
	publisher := &publisherSpy{}
	dispatcher := kernel.OutboxDispatcher{Store: store, Publisher: publisher, Clock: kernel.NewFixedClock(fixedTime)}
	count, err := dispatcher.Dispatch(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("Dispatch = %d, %v", count, err)
	}
	record := readOutbox(t, store)[0]
	if record.State != kernel.OutboxPublished || record.PublishedAt == nil || len(publisher.events) != 1 {
		t.Fatalf("unexpected dispatch state: record=%+v published=%d", record, len(publisher.events))
	}
}

func TestOutbox_PublishFailureLeavesEventPending(t *testing.T) {
	store := memory.NewStore()
	appendEvent(t, store, sampleEvent("evt_1", nil))
	publisher := &publisherSpy{failures: 1}
	dispatcher := kernel.OutboxDispatcher{Store: store, Publisher: publisher, Clock: kernel.NewFixedClock(fixedTime)}
	count, err := dispatcher.Dispatch(context.Background(), 10)
	if err == nil || count != 0 {
		t.Fatalf("Dispatch = %d, %v, want failure", count, err)
	}
	record := readOutbox(t, store)[0]
	if record.State != kernel.OutboxPending || record.Attempts != 1 || !strings.Contains(record.LastError, "broker unavailable") {
		t.Fatalf("record = %+v", record)
	}
}

func TestOutbox_ExpiredLeaseCanBeReclaimed(t *testing.T) {
	store := memory.NewStore()
	clock := kernel.NewFixedClock(fixedTime)
	appendEvent(t, store, sampleEvent("evt_1", nil))
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		_, err := tx.ClaimOutbox(context.Background(), 1, clock.Now(), time.Second)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	publisher := &publisherSpy{}
	count, err := (kernel.OutboxDispatcher{Store: store, Publisher: publisher, Clock: clock}).Dispatch(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("reclaimed Dispatch = %d, %v", count, err)
	}
}

func TestInbox_DuplicateEventIsIgnored(t *testing.T) {
	store := memory.NewStore()
	processor := kernel.InboxProcessor{Store: store, Clock: kernel.NewFixedClock(fixedTime)}
	event := sampleEvent("evt_1", map[string]any{"value": 1})
	var calls atomic.Int32
	handler := func(_ context.Context, _ kernel.Tx, _ kernelv1.DomainEventEnvelope[json.RawMessage]) error {
		calls.Add(1)
		return nil
	}
	first, err := processor.Process(context.Background(), event, handler)
	if err != nil || first.Duplicate {
		t.Fatalf("first Process = %+v, %v", first, err)
	}
	second, err := processor.Process(context.Background(), event, handler)
	if err != nil || !second.Duplicate {
		t.Fatalf("second Process = %+v, %v", second, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestInbox_SameEventIDWithDifferentPayloadIsRejected(t *testing.T) {
	store := memory.NewStore()
	processor := kernel.InboxProcessor{Store: store, Clock: kernel.NewFixedClock(fixedTime)}
	handler := func(_ context.Context, _ kernel.Tx, _ kernelv1.DomainEventEnvelope[json.RawMessage]) error {
		return nil
	}
	if _, err := processor.Process(context.Background(), sampleEvent("evt_1", map[string]any{"value": 1}), handler); err != nil {
		t.Fatal(err)
	}
	_, err := processor.Process(context.Background(), sampleEvent("evt_1", map[string]any{"value": 2}), handler)
	if kernel.ErrorCode(err) != kernelv1.CodeConflict {
		t.Fatalf("error = %v, want CONFLICT", err)
	}
}

func TestInbox_HandlerFailureRollsBackDeduplicationRecord(t *testing.T) {
	store := memory.NewStore()
	processor := kernel.InboxProcessor{Store: store, Clock: kernel.NewFixedClock(fixedTime)}
	event := sampleEvent("evt_1", nil)
	sentinel := errors.New("consumer failed")
	if _, err := processor.Process(context.Background(), event, func(context.Context, kernel.Tx, kernelv1.DomainEventEnvelope[json.RawMessage]) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Process error = %v", err)
	}
	var calls atomic.Int32
	result, err := processor.Process(context.Background(), event, func(context.Context, kernel.Tx, kernelv1.DomainEventEnvelope[json.RawMessage]) error {
		calls.Add(1)
		return nil
	})
	if err != nil || result.Duplicate || calls.Load() != 1 {
		t.Fatalf("retry = %+v, err=%v, calls=%d", result, err, calls.Load())
	}
}

func TestAudit_EveryMutationProducesAuditRecord(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create", "Acme")
	_, err := h.service.InviteMember(context.Background(), kernel.InviteMemberCommand{
		Meta: meta(user("owner", "kernel:*"), "invite"), OrganizationID: created.Organization.OrganizationID, PrincipalID: "developer", Role: kernel.RoleDeveloper,
	})
	if err != nil {
		t.Fatal(err)
	}
	records := readAudit(t, h.store, created.Organization.OrganizationID)
	actions := make([]string, 0, len(records))
	for _, record := range records {
		actions = append(actions, record.Action)
	}
	sort.Strings(actions)
	if !contains(actions, kernel.ActionOrganizationCreate) || !contains(actions, kernel.ActionMembershipInvite) {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestAudit_RecordContainsActorTenantCorrelationAndOutcome(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create", "Acme")
	records := readAudit(t, h.store, created.Organization.OrganizationID)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Actor.PrincipalID != "owner" || record.TenantID != created.Organization.OrganizationID || record.CorrelationID != "cor_create" || record.Outcome != kernelv1.AuditOutcomeSucceeded {
		t.Fatalf("record = %+v", record)
	}
}

func TestAudit_IsAppendOnlyAtRepositoryBoundary(t *testing.T) {
	txType := reflect.TypeOf((*kernel.Tx)(nil)).Elem()
	for _, forbiddenMethod := range []string{"UpdateAudit", "DeleteAudit", "ReplaceAudit"} {
		if _, exists := txType.MethodByName(forbiddenMethod); exists {
			t.Fatalf("kernel.Tx exposes forbidden method %s", forbiddenMethod)
		}
	}
	if _, exists := txType.MethodByName("AppendAudit"); !exists {
		t.Fatal("kernel.Tx does not expose AppendAudit")
	}
}

func TestAudit_RedactsSensitiveFields(t *testing.T) {
	metadata := map[string]any{
		"safe":  "visible",
		"token": "top-secret-token",
		"nested": map[string]any{
			"database_password": "hunter2",
			"items":             []any{map[string]any{"api-key": "key-123"}},
		},
	}
	redacted := kernel.RedactMetadata(metadata)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, secret := range []string{"top-secret-token", "hunter2", "key-123"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q leaked in %s", secret, output)
		}
	}
	if !strings.Contains(output, "visible") || strings.Count(output, "[REDACTED]") < 3 {
		t.Fatalf("redacted metadata = %s", output)
	}
	if metadata["token"] != "top-secret-token" {
		t.Fatal("redaction mutated input")
	}
}

func TestAudit_FailedAuthorizationIsRecorded(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create", "Acme")
	_, err := h.service.GetOrganization(context.Background(), user("intruder", "kernel:*"), created.Organization.OrganizationID, "cor_denied")
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("read error = %v", err)
	}
	records := readAudit(t, h.store, created.Organization.OrganizationID)
	found := false
	for _, record := range records {
		if record.CorrelationID == "cor_denied" && record.Outcome == kernelv1.AuditOutcomeDenied && record.ErrorCode == kernelv1.CodeForbidden {
			found = true
		}
	}
	if !found {
		t.Fatalf("denied audit record not found: %+v", records)
	}
}

func TestPublicError_RedactsInternalCause(t *testing.T) {
	secretCause := errors.New("dial postgres://admin:secret@internal-db:5432/platform")
	public := kernel.ToPublicError(kernel.WrapError(kernelv1.CodeInternal, "Database unavailable", true, secretCause), "op_1")
	encoded, _ := json.Marshal(public)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "internal-db") || strings.Contains(string(encoded), "postgres://") {
		t.Fatalf("internal cause leaked: %s", encoded)
	}
	if public.Code != kernelv1.CodeInternal || public.OperationID != "op_1" || !public.Retryable {
		t.Fatalf("public error = %+v", public)
	}
}

func TestPublicError_RetryableFlagMatchesPolicy(t *testing.T) {
	conflict := kernel.ToPublicError(kernel.NewError(kernelv1.CodeConflict, "conflict"), "")
	transient := kernel.ToPublicError(kernel.WrapError(kernelv1.CodeInternal, "temporary", true, errors.New("timeout")), "")
	if conflict.Retryable || !transient.Retryable {
		t.Fatalf("retryable policy mismatch: conflict=%v transient=%v", conflict.Retryable, transient.Retryable)
	}
}

func sampleEvent(id string, payload any) kernelv1.DomainEventEnvelope[json.RawMessage] {
	encoded, _ := json.Marshal(payload)
	return kernelv1.DomainEventEnvelope[json.RawMessage]{
		EventID:       id,
		Type:          "kernel.test.v1",
		Version:       1,
		TenantID:      "org_1",
		AggregateID:   "aggregate_1",
		CorrelationID: "cor_1",
		OccurredAt:    fixedTime,
		Payload:       encoded,
	}
}

func appendEvent(t *testing.T, store *memory.Store, event kernelv1.DomainEventEnvelope[json.RawMessage]) {
	t.Helper()
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		return tx.AppendOutbox(context.Background(), kernel.OutboxRecord{Event: event, State: kernel.OutboxPending, CreatedAt: fixedTime})
	}); err != nil {
		t.Fatal(err)
	}
}

func readOutbox(t *testing.T, store *memory.Store) []kernel.OutboxRecord {
	t.Helper()
	var records []kernel.OutboxRecord
	if err := store.View(context.Background(), func(reader kernel.Reader) error {
		var err error
		records, err = reader.ListOutbox(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return records
}

func readAudit(t *testing.T, store *memory.Store, tenantID kernelv1.TenantID) []kernelv1.AuditEnvelope {
	t.Helper()
	var records []kernelv1.AuditEnvelope
	if err := store.View(context.Background(), func(reader kernel.Reader) error {
		var err error
		records, err = reader.ListAudit(context.Background(), tenantID, 1000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return records
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestIdempotency_FailedRetryableOperationCanResume(t *testing.T) {
	store := memory.NewStore()
	record := kernel.IdempotencyRecord{
		Scope:       "tenant/org_1/owner/Test",
		Key:         "retryable",
		Fingerprint: "fingerprint",
		Status:      kernel.IdempotencyStarted,
		CreatedAt:   fixedTime,
		UpdatedAt:   fixedTime,
	}
	sentinel := errors.New("temporary transaction failure")
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		_, claimed, err := tx.ClaimIdempotency(context.Background(), record)
		if err != nil {
			return err
		}
		if !claimed {
			t.Fatal("initial claim was not acquired")
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("first transaction error = %v", err)
	}
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		_, claimed, err := tx.ClaimIdempotency(context.Background(), record)
		if err != nil {
			return err
		}
		if !claimed {
			t.Fatal("rolled-back claim blocked retry")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOperationService_CancelIsAuthorizedAndIdempotent(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create-cancel-org", "Cancel Corp")
	orgID := created.Organization.OrganizationID
	started, err := h.service.Start(context.Background(), kernelv1.CommandMeta{
		TenantID:       orgID,
		Principal:      user("owner", "kernel:*"),
		CorrelationID:  "cor_cancel_target",
		IdempotencyKey: "start-cancel-target",
		Command:        "LongRunningTask",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := kernel.CancelOperationCommand{
		Meta:        meta(user("owner", "kernel:*"), "cancel-target"),
		OperationID: started.OperationID,
	}
	first, err := h.service.CancelOperation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.CancelOperation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Operation != second.Operation || first.Operation.State != kernelv1.OperationCanceled {
		t.Fatalf("cancel results differ or are not canceled: first=%+v second=%+v", first, second)
	}
	snapshot, err := h.service.Get(context.Background(), started.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != kernelv1.OperationCanceled {
		t.Fatalf("operation state = %s, want CANCELED", snapshot.State)
	}
}

func TestOperationService_DeniedCancelCreatesTenantScopedFailureAndAudit(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create-denied-cancel", "Denied Cancel")
	orgID := created.Organization.OrganizationID
	_, err := h.service.InviteMember(context.Background(), kernel.InviteMemberCommand{
		Meta:           meta(user("owner", "kernel:*"), "invite-dev-cancel"),
		OrganizationID: orgID,
		PrincipalID:    "developer",
		Role:           kernel.RoleDeveloper,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.AcceptInvitation(context.Background(), kernel.AcceptInvitationCommand{
		Meta:           meta(user("developer", "kernel:*"), "accept-dev-cancel"),
		OrganizationID: orgID,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := h.service.Start(context.Background(), kernelv1.CommandMeta{
		TenantID:       orgID,
		Principal:      user("owner", "kernel:*"),
		CorrelationID:  "cor_start_denied_cancel",
		IdempotencyKey: "start-denied-cancel",
		Command:        "ProtectedTask",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.CancelOperation(context.Background(), kernel.CancelOperationCommand{
		Meta:        meta(user("developer", "kernel:*"), "denied-cancel"),
		OperationID: started.OperationID,
	})
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("cancel error = %v, want FORBIDDEN", err)
	}

	records := readAudit(t, h.store, orgID)
	var denied *kernelv1.AuditEnvelope
	for i := range records {
		if records[i].CorrelationID == "cor_denied-cancel" && records[i].Action == kernel.ActionOperationCancel {
			copy := records[i]
			denied = &copy
		}
	}
	if denied == nil || denied.TenantID != orgID || denied.Outcome != kernelv1.AuditOutcomeDenied {
		t.Fatalf("tenant-scoped denied audit not found: %+v", records)
	}

	var failedOperation kernelv1.OperationSnapshot
	if err := h.store.View(context.Background(), func(reader kernel.Reader) error {
		outbox, err := reader.ListOutbox(context.Background())
		if err != nil {
			return err
		}
		for _, record := range outbox {
			if record.Event.Type != kernel.EventOperationChanged || record.Event.TenantID != orgID {
				continue
			}
			var payload struct {
				OperationID kernelv1.OperationID    `json:"operation_id"`
				State       kernelv1.OperationState `json:"state"`
			}
			if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
				return err
			}
			if payload.State == kernelv1.OperationFailed {
				op, err := reader.GetOperation(context.Background(), payload.OperationID)
				if err != nil {
					return err
				}
				failedOperation = op.Snapshot()
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if failedOperation.OperationID == "" || failedOperation.TenantID != orgID || failedOperation.State != kernelv1.OperationFailed {
		t.Fatalf("failed command operation is not tenant scoped: %+v", failedOperation)
	}
}

func TestMembership_ServiceLifecycleInviteAcceptChangeSuspendRemove(t *testing.T) {
	h := newHarness(t)
	created := createOrganization(t, h, "owner", "create-membership-lifecycle", "Lifecycle")
	orgID := created.Organization.OrganizationID
	owner := user("owner", "kernel:*")
	if _, err := h.service.InviteMember(context.Background(), kernel.InviteMemberCommand{
		Meta: meta(owner, "lifecycle-invite"), OrganizationID: orgID, PrincipalID: "member", Role: kernel.RoleViewer,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.AcceptInvitation(context.Background(), kernel.AcceptInvitationCommand{
		Meta: meta(user("member", "kernel:*"), "lifecycle-accept"), OrganizationID: orgID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ChangeMemberRole(context.Background(), kernel.ChangeMemberRoleCommand{
		Meta: meta(owner, "lifecycle-role"), OrganizationID: orgID, PrincipalID: "member", Role: kernel.RoleDeveloper,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.SuspendMembership(context.Background(), kernel.SuspendMembershipCommand{
		Meta: meta(owner, "lifecycle-suspend"), OrganizationID: orgID, PrincipalID: "member",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RemoveMembership(context.Background(), kernel.RemoveMembershipCommand{
		Meta: meta(owner, "lifecycle-remove"), OrganizationID: orgID, PrincipalID: "member",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.service.GetOrganization(context.Background(), owner, orgID, "cor_lifecycle_read")
	if err != nil {
		t.Fatal(err)
	}
	for _, membership := range snapshot.Memberships {
		if membership.PrincipalID == "member" {
			if membership.Role != kernel.RoleDeveloper || membership.State != kernel.MembershipRemoved {
				t.Fatalf("membership = %+v", membership)
			}
			return
		}
	}
	t.Fatal("member not found")
}

func TestPublicError_MapsDomainErrorToStableCode(t *testing.T) {
	public := kernel.ToPublicError(kernel.NewError(kernelv1.CodeLastOwner, "organization must retain one owner"), "op_stable")
	if public.Code != kernelv1.CodeLastOwner || public.Message != "organization must retain one owner" || public.OperationID != "op_stable" {
		t.Fatalf("public error = %+v", public)
	}
}

func TestOutbox_PublishSucceededButMarkFailedRetriesAtLeastOnce(t *testing.T) {
	inner := memory.NewStore()
	appendEvent(t, inner, sampleEvent("evt_at_least_once", nil))
	store := &failMarkPublishedStore{Store: inner}
	store.failNext.Store(true)
	clock := kernel.NewFixedClock(fixedTime)
	publisher := &publisherSpy{}
	dispatcher := kernel.OutboxDispatcher{Store: store, Publisher: publisher, Clock: clock, Lease: time.Second}
	count, err := dispatcher.Dispatch(context.Background(), 1)
	if err == nil || count != 0 {
		t.Fatalf("first dispatch = %d, %v, want post-publish marking failure", count, err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(publisher.events))
	}
	clock.Advance(2 * time.Second)
	count, err = dispatcher.Dispatch(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("retry dispatch = %d, %v", count, err)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("at-least-once retry did not republish: calls=%d", len(publisher.events))
	}
	record := readOutbox(t, inner)[0]
	if record.State != kernel.OutboxPublished {
		t.Fatalf("record state = %s", record.State)
	}
}

func TestInbox_ConcurrentDuplicateInvokesHandlerOnce(t *testing.T) {
	store := memory.NewStore()
	processor := kernel.InboxProcessor{Store: store, Clock: kernel.NewFixedClock(fixedTime)}
	event := sampleEvent("evt_concurrent_inbox", map[string]any{"value": 1})
	var calls atomic.Int32
	const workers = 64
	var group sync.WaitGroup
	errorsCh := make(chan error, workers)
	duplicates := make(chan bool, workers)
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			result, err := processor.Process(context.Background(), event, func(context.Context, kernel.Tx, kernelv1.DomainEventEnvelope[json.RawMessage]) error {
				calls.Add(1)
				return nil
			})
			if err != nil {
				errorsCh <- err
				return
			}
			duplicates <- result.Duplicate
		}()
	}
	group.Wait()
	close(errorsCh)
	close(duplicates)
	for err := range errorsCh {
		t.Errorf("concurrent inbox processing: %v", err)
	}
	duplicateCount := 0
	for duplicate := range duplicates {
		if duplicate {
			duplicateCount++
		}
	}
	if calls.Load() != 1 || duplicateCount != workers-1 {
		t.Fatalf("handler calls=%d duplicate results=%d", calls.Load(), duplicateCount)
	}
}

type failMarkPublishedStore struct {
	kernel.Store
	failNext atomic.Bool
}

func (s *failMarkPublishedStore) Transact(ctx context.Context, fn func(kernel.Tx) error) error {
	return s.Store.Transact(ctx, func(tx kernel.Tx) error {
		return fn(&failMarkPublishedTx{Tx: tx, parent: s})
	})
}

type failMarkPublishedTx struct {
	kernel.Tx
	parent *failMarkPublishedStore
}

func (tx *failMarkPublishedTx) MarkOutboxPublished(ctx context.Context, eventID string, now time.Time) error {
	if tx.parent.failNext.CompareAndSwap(true, false) {
		return errors.New("simulated database failure after broker publish")
	}
	return tx.Tx.MarkOutboxPublished(ctx, eventID, now)
}
