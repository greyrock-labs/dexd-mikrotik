package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/doonga/dexd-mikrotik/internal/config"
	"github.com/doonga/dexd-mikrotik/internal/dns"
	"github.com/doonga/dexd-mikrotik/internal/registry"
	"github.com/doonga/dexd-mikrotik/internal/source"
	"github.com/prometheus/client_golang/prometheus"
)

// ---- fakes ----

type fakeSource struct {
	endpoints []*source.Endpoint
	err       error
	eventCh   chan Event
	errCh     chan error
}

func (f *fakeSource) Endpoints(_ context.Context) ([]*source.Endpoint, error) {
	return f.endpoints, f.err
}

func (f *fakeSource) Events(_ context.Context) (<-chan Event, <-chan error) {
	if f.eventCh == nil {
		ev := make(chan Event)
		errCh := make(chan error)
		close(ev)
		close(errCh)
		return ev, errCh
	}
	return f.eventCh, f.errCh
}

type providerCall struct {
	Op     string
	Record dns.Record
}

type fakeProvider struct {
	mu      sync.Mutex
	initial []dns.Record
	calls   []providerCall
	nextID  int
	failOn  map[string]error // keyed by "op:key"
}

// snapshot returns a copy of calls safe to read from a different goroutine.
func (f *fakeProvider) snapshot() []providerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]providerCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeProvider) failKey(op, key string) error {
	if f.failOn == nil {
		return nil
	}
	return f.failOn[op+":"+key]
}

func (f *fakeProvider) ListRecords(_ context.Context) ([]dns.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, providerCall{Op: "list"})
	return append([]dns.Record(nil), f.initial...), nil
}

func (f *fakeProvider) CreateRecord(_ context.Context, r dns.Record) (dns.Record, error) {
	err := f.failKey("create", r.Name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.calls = append(f.calls, providerCall{Op: "create", Record: r})
		return dns.Record{}, err
	}
	f.nextID++
	r.ID = fmt.Sprintf("id-%d", f.nextID)
	f.calls = append(f.calls, providerCall{Op: "create", Record: r})
	return r, nil
}

func (f *fakeProvider) UpdateRecord(_ context.Context, r dns.Record) (dns.Record, error) {
	err := f.failKey("update", r.Name)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, providerCall{Op: "update", Record: r})
	if err != nil {
		return dns.Record{}, err
	}
	return r, nil
}

func (f *fakeProvider) DeleteRecord(_ context.Context, id, key, recordType string) error {
	err := f.failKey("delete", key)
	r := dns.Record{ID: id, Name: key, Type: recordType}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, providerCall{Op: "delete", Record: r})
	return err
}

// ---- helpers ----

const testOwner = "us"

func ep(name, target string) *source.Endpoint {
	return epType(name, target, "A")
}

func epType(name, target, recordType string) *source.Endpoint {
	return &source.Endpoint{
		DNSName:    name,
		Target:     target,
		RecordType: recordType,
		OwnerID:    testOwner,
		Resource:   "docker/" + name,
	}
}

func aRec(id, key, value string) dns.Record {
	return dns.Record{ID: id, Name: key, Type: "A", Value: value}
}

func ownedTXT(id, hostname, owner string) dns.Record {
	return ownedTXTType("", id, "A", hostname, owner)
}

func ownedTXTType(prefix, id, recordType, hostname, owner string) dns.Record {
	return dns.Record{
		ID:    id,
		Name:  registry.TXTKey(prefix, recordType, hostname),
		Type:  "TXT",
		Value: registry.EncodeTXT(owner, "docker/"+hostname),
	}
}

func opKeys(calls []providerCall, op string) []string {
	var out []string
	for _, c := range calls {
		if c.Op == op {
			out = append(out, c.Record.Name)
		}
	}
	return out
}

func countOp(calls []providerCall, op string) int {
	n := 0
	for _, c := range calls {
		if c.Op == op {
			n++
		}
	}
	return n
}

func newCtrl(src *fakeSource, prov *fakeProvider) *Controller {
	return New(src, prov, testOwner, "", config.PolicySync, time.Hour)
}

func newCtrlPolicy(src *fakeSource, prov *fakeProvider, policy config.Policy) *Controller {
	return New(src, prov, testOwner, "", policy, time.Hour)
}

// ---- reconcile tests ----

func TestReconcile_CreatesNewPair(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{}
	newCtrl(src, prov).reconcile(context.Background())

	creates := opKeys(prov.calls, "create")
	if !containsAll(creates, []string{"foo.example.com", "a-foo.example.com"}) {
		t.Errorf("expected creates for A and TXT, got %v", creates)
	}
	assertBefore(t, prov.calls, "create", "a-foo.example.com", "create", "foo.example.com")
	if countOp(prov.calls, "update") != 0 || countOp(prov.calls, "delete") != 0 {
		t.Errorf("unexpected update/delete calls: %v", prov.calls)
	}
}

func TestReconcile_CreatesCNAMEPair(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{epType("foo.example.com", "traefik.example.com", "CNAME")}}
	prov := &fakeProvider{}
	newCtrl(src, prov).reconcile(context.Background())

	creates := opKeys(prov.calls, "create")
	if !containsAll(creates, []string{"foo.example.com", "cname-foo.example.com"}) {
		t.Errorf("expected creates for CNAME and TXT, got %v", creates)
	}
	assertBefore(t, prov.calls, "create", "cname-foo.example.com", "create", "foo.example.com")
	for _, c := range prov.calls {
		if c.Op == "create" && c.Record.Name == "foo.example.com" {
			if c.Record.Type != "CNAME" {
				t.Errorf("RecordType = %q, want CNAME", c.Record.Type)
			}
			if c.Record.Value != "traefik.example.com" {
				t.Errorf("Value = %q, want traefik.example.com", c.Record.Value)
			}
		}
	}
}

func TestReconcile_NoOpWhenMatching(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", testOwner),
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	if countOp(prov.calls, "create") != 0 || countOp(prov.calls, "update") != 0 || countOp(prov.calls, "delete") != 0 {
		t.Errorf("expected no-op, got calls: %v", prov.calls)
	}
}

func TestReconcile_UpdatesOwnedPair(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.2")}}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", testOwner),
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	updates := opKeys(prov.calls, "update")
	if !containsAll(updates, []string{"foo.example.com", "a-foo.example.com"}) {
		t.Errorf("expected updates for A and TXT, got %v", updates)
	}
	assertBefore(t, prov.calls, "update", "a-foo.example.com", "update", "foo.example.com")
	// Verify the A record gets the new IP.
	for _, c := range prov.calls {
		if c.Op == "update" && c.Record.Name == "foo.example.com" && c.Record.Value != "10.0.0.2" {
			t.Errorf("A record updated with wrong value: %q", c.Record.Value)
		}
	}
	if countOp(prov.calls, "create") != 0 || countOp(prov.calls, "delete") != 0 {
		t.Errorf("unexpected create/delete calls: %v", prov.calls)
	}
}

func TestReconcile_DeletesOwnedPair(t *testing.T) {
	src := &fakeSource{endpoints: nil}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", testOwner),
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	deletes := opKeys(prov.calls, "delete")
	if !containsAll(deletes, []string{"foo.example.com", "a-foo.example.com"}) {
		t.Errorf("expected deletes for A and TXT, got %v", deletes)
	}
	if countOp(prov.calls, "create") != 0 || countOp(prov.calls, "update") != 0 {
		t.Errorf("unexpected create/update calls: %v", prov.calls)
	}
}

func TestReconcile_SkipsUnownedRecord(t *testing.T) {
	src := &fakeSource{endpoints: nil}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			// no TXT — not managed by us
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	if countOp(prov.calls, "delete") != 0 {
		t.Errorf("must not delete unowned record, got: %v", prov.calls)
	}
}

func TestReconcile_SkipsForeignOwnedRecord(t *testing.T) {
	src := &fakeSource{endpoints: nil}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", "someone-else"),
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	if countOp(prov.calls, "delete") != 0 {
		t.Errorf("must not delete record owned by another agent, got: %v", prov.calls)
	}
}

func TestReconcile_RecreateMissingA_UpsertsTXT(t *testing.T) {
	// Orphan TXT exists (we own it), A record is missing, hostname still desired.
	// Expect: CreateRecord for A, UpdateRecord (not CreateRecord) for the TXT.
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{
		initial: []dns.Record{
			ownedTXT("t1", "foo.example.com", testOwner), // TXT present, A missing
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	creates := opKeys(prov.calls, "create")
	updates := opKeys(prov.calls, "update")
	if !containsAll(creates, []string{"foo.example.com"}) {
		t.Errorf("expected create for A record, got creates: %v", creates)
	}
	if containsAll(creates, []string{"a-foo.example.com"}) {
		t.Errorf("TXT should be updated, not created; got creates: %v", creates)
	}
	if !containsAll(updates, []string{"a-foo.example.com"}) {
		t.Errorf("expected update for orphan TXT, got updates: %v", updates)
	}
	assertBefore(t, prov.calls, "update", "a-foo.example.com", "create", "foo.example.com")
}

func TestReconcile_DoesNotCreateRecordWhenTXTCreateFails(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{
		failOn: map[string]error{"create:a-foo.example.com": fmt.Errorf("injected error")},
	}
	newCtrl(src, prov).reconcile(context.Background())

	if containsAll(opKeys(prov.calls, "create"), []string{"foo.example.com"}) {
		t.Errorf("A record must not be created after TXT create fails, got calls: %v", prov.calls)
	}
}

func TestReconcile_ReplacesOwnedRecordTypeBeforeCreate(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{epType("foo.example.com", "target.example.com", "CNAME")}}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", testOwner),
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	assertBefore(t, prov.calls, "delete", "foo.example.com", "create", "cname-foo.example.com")
	assertBefore(t, prov.calls, "delete", "a-foo.example.com", "create", "cname-foo.example.com")
	assertBefore(t, prov.calls, "create", "cname-foo.example.com", "create", "foo.example.com")

	deletes := opKeys(prov.calls, "delete")
	if !containsAll(deletes, []string{"foo.example.com", "a-foo.example.com"}) {
		t.Errorf("expected old A pair to be deleted, got deletes: %v", deletes)
	}
	creates := opKeys(prov.calls, "create")
	if !containsAll(creates, []string{"foo.example.com", "cname-foo.example.com"}) {
		t.Errorf("expected new CNAME pair to be created, got creates: %v", creates)
	}
}

func TestReconcile_DeletesOrphanTXT(t *testing.T) {
	// Orphan TXT exists (we own it), A record is missing, hostname not desired.
	// Expect: one delete call for the TXT with RecordType=TXT.
	src := &fakeSource{endpoints: nil}
	prov := &fakeProvider{
		initial: []dns.Record{
			ownedTXT("t1", "foo.example.com", testOwner), // TXT present, A missing, not desired
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	if countOp(prov.calls, "create") != 0 || countOp(prov.calls, "update") != 0 {
		t.Errorf("expected no creates or updates, got: %v", prov.calls)
	}
	deletes := opKeys(prov.calls, "delete")
	if !containsAll(deletes, []string{"a-foo.example.com"}) {
		t.Errorf("expected delete for orphan TXT a-foo.example.com, got: %v", deletes)
	}
	for _, c := range prov.calls {
		if c.Op == "delete" && c.Record.Name == "a-foo.example.com" {
			if c.Record.Type != "TXT" {
				t.Errorf("orphan TXT delete has RecordType = %q, want TXT", c.Record.Type)
			}
		}
	}
}

func TestReconcile_UpsertOnlyDoesNotDeleteStalePairOrOrphanTXT(t *testing.T) {
	src := &fakeSource{endpoints: nil}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", testOwner),
			ownedTXT("t2", "orphan.example.com", testOwner),
		},
	}
	newCtrlPolicy(src, prov, config.PolicyUpsertOnly).reconcile(context.Background())

	if countOp(prov.calls, "delete") != 0 {
		t.Fatalf("upsert-only must not delete stale records or orphan TXT, got: %v", prov.calls)
	}
}

func TestReconcile_UpsertOnlyUpdatesAndReplaces(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.2")}}
		prov := &fakeProvider{
			initial: []dns.Record{
				aRec("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", testOwner),
			},
		}
		newCtrlPolicy(src, prov, config.PolicyUpsertOnly).reconcile(context.Background())

		if !containsAll(opKeys(prov.calls, "update"), []string{"foo.example.com", "a-foo.example.com"}) {
			t.Fatalf("upsert-only should update owned records, got calls: %v", prov.calls)
		}
	})

	t.Run("replace", func(t *testing.T) {
		src := &fakeSource{endpoints: []*source.Endpoint{epType("foo.example.com", "target.example.com", "CNAME")}}
		prov := &fakeProvider{
			initial: []dns.Record{
				aRec("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", testOwner),
			},
		}
		newCtrlPolicy(src, prov, config.PolicyUpsertOnly).reconcile(context.Background())

		if !containsAll(opKeys(prov.calls, "delete"), []string{"foo.example.com", "a-foo.example.com"}) {
			t.Fatalf("upsert-only should allow replacement deletes, got calls: %v", prov.calls)
		}
		if !containsAll(opKeys(prov.calls, "create"), []string{"foo.example.com", "cname-foo.example.com"}) {
			t.Fatalf("upsert-only should create replacement pair, got calls: %v", prov.calls)
		}
	})
}

func TestReconcile_CreateOnlyCreatesOnly(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{
		ep("new.example.com", "10.0.0.2"),
		ep("foo.example.com", "10.0.0.2"),
		epType("flip.example.com", "target.example.com", "CNAME"),
	}}
	prov := &fakeProvider{
		initial: []dns.Record{
			aRec("a1", "foo.example.com", "10.0.0.1"),
			ownedTXT("t1", "foo.example.com", testOwner),
			aRec("a2", "stale.example.com", "10.0.0.1"),
			ownedTXT("t2", "stale.example.com", testOwner),
			aRec("a3", "flip.example.com", "10.0.0.1"),
			ownedTXT("t3", "flip.example.com", testOwner),
		},
	}
	newCtrlPolicy(src, prov, config.PolicyCreateOnly).reconcile(context.Background())

	if !containsAll(opKeys(prov.calls, "create"), []string{"new.example.com", "a-new.example.com"}) {
		t.Fatalf("create-only should create missing pair, got calls: %v", prov.calls)
	}
	if countOp(prov.calls, "update") != 0 || countOp(prov.calls, "delete") != 0 {
		t.Fatalf("create-only must not update or delete, got calls: %v", prov.calls)
	}
	if containsAll(opKeys(prov.calls, "create"), []string{"flip.example.com"}) {
		t.Fatalf("create-only must not replace type flips, got calls: %v", prov.calls)
	}
}

func TestReconcile_CreateOnlyDoesNotUpdateOwnedOrphanTXT(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{
		initial: []dns.Record{
			ownedTXT("t1", "foo.example.com", testOwner),
		},
	}
	newCtrlPolicy(src, prov, config.PolicyCreateOnly).reconcile(context.Background())

	if !containsAll(opKeys(prov.calls, "create"), []string{"foo.example.com"}) {
		t.Fatalf("create-only should create missing A record, got calls: %v", prov.calls)
	}
	if countOp(prov.calls, "update") != 0 {
		t.Fatalf("create-only must not update existing TXT, got calls: %v", prov.calls)
	}
}

func TestReconcile_DoesNotClaimForeignOrphanTXT(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{
		initial: []dns.Record{
			ownedTXT("t1", "foo.example.com", "someone-else"),
		},
	}
	newCtrl(src, prov).reconcile(context.Background())

	// Another owner's TXT makes the hostname a conflict: nothing is written.
	if n := countOp(prov.calls, "create") + countOp(prov.calls, "update") + countOp(prov.calls, "delete"); n != 0 {
		t.Fatalf("must not write at a hostname with a foreign TXT, got calls: %v", prov.calls)
	}
	if got := planChangesGauge(t, "conflict"); got != 1 {
		t.Errorf("dexd_mikrotik_plan_changes{operation=\"conflict\"} = %v, want 1", got)
	}
}

func TestReconcile_ContinuesAfterCreateError(t *testing.T) {
	src := &fakeSource{endpoints: []*source.Endpoint{
		ep("foo.example.com", "10.0.0.1"),
		ep("bar.example.com", "10.0.0.1"),
	}}
	prov := &fakeProvider{
		failOn: map[string]error{"create:foo.example.com": fmt.Errorf("injected error")},
	}
	newCtrl(src, prov).reconcile(context.Background())

	// Both hostnames must have been attempted despite the first error.
	attempted := make(map[string]bool)
	for _, c := range prov.calls {
		if c.Op == "create" {
			attempted[c.Record.Name] = true
		}
	}
	if !attempted["foo.example.com"] {
		t.Error("foo.example.com create was not attempted")
	}
	if !attempted["bar.example.com"] {
		t.Error("bar.example.com create was not attempted after foo failure")
	}
}

// TestRun_DebouncesEventsAndReconciles verifies that a Docker event
// triggers a second reconcile after the 2s debounce window.
func TestRun_DebouncesEventsAndReconciles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping debounce test in short mode (needs ~3s)")
	}

	evCh := make(chan Event, 1)
	errCh := make(chan error)
	src := &fakeSource{
		endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")},
		eventCh:   evCh,
		errCh:     errCh,
	}
	prov := &fakeProvider{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ctrl := New(src, prov, testOwner, "", config.PolicySync, time.Hour)
	go ctrl.Run(ctx)

	// Emit one event to trigger the debounce path.
	evCh <- Event{Action: "start", Name: "whoami"}

	// Poll until we see at least 2 list calls (initial + post-debounce).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countOp(prov.snapshot(), "list") >= 2 {
			cancel()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("expected ≥2 list calls after debounce, got %d", countOp(prov.snapshot(), "list"))
}

func TestReconcile_RespectsTXTPrefix(t *testing.T) {
	const prefix = "userprefix."
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}
	prov := &fakeProvider{}
	ctrl := New(src, prov, testOwner, prefix, config.PolicySync, time.Hour)
	ctrl.reconcile(context.Background())

	creates := opKeys(prov.calls, "create")
	wantTXTKey := "userprefix.a-foo.example.com"
	if !containsAll(creates, []string{"foo.example.com", wantTXTKey}) {
		t.Errorf("expected TXT key %q with prefix, got creates: %v", wantTXTKey, creates)
	}
}

// ---- set helper ----

func containsAll(haystack, needles []string) bool {
	m := make(map[string]bool, len(haystack))
	for _, s := range haystack {
		m[s] = true
	}
	for _, n := range needles {
		if !m[n] {
			return false
		}
	}
	return true
}

func assertBefore(t *testing.T, calls []providerCall, firstOp, firstKey, secondOp, secondKey string) {
	t.Helper()

	first := callIndex(calls, firstOp, firstKey)
	second := callIndex(calls, secondOp, secondKey)
	if first < 0 {
		t.Fatalf("missing %s call for %s in %v", firstOp, firstKey, calls)
	}
	if second < 0 {
		t.Fatalf("missing %s call for %s in %v", secondOp, secondKey, calls)
	}
	if first >= second {
		t.Fatalf("expected %s %s before %s %s, got calls: %v", firstOp, firstKey, secondOp, secondKey, calls)
	}
}

func callIndex(calls []providerCall, op, key string) int {
	for i, c := range calls {
		if c.Op == op && c.Record.Name == key {
			return i
		}
	}
	return -1
}

// ---- shared-router ownership tests ----

func externalDNSTXT(id, hostname string) dns.Record {
	return dns.Record{
		ID:    id,
		Name:  registry.TXTKey("", "A", hostname),
		Type:  "TXT",
		Value: "heritage=external-dns,external-dns/owner=default,external-dns/resource=ingress/x/y",
	}
}

func TestUpsertTXT_RefusesWhenForeignTXTPresent(t *testing.T) {
	// Defensive: even if the plan let it through, never create next to a
	// foreign ownership TXT.
	prov := &fakeProvider{}
	ctrl := newCtrl(&fakeSource{}, prov)
	current := []dns.Record{externalDNSTXT("t-foreign", "foo.example.com")}

	if err := ctrl.upsertTXT(context.Background(), ep("foo.example.com", "192.0.2.1"), current); err == nil {
		t.Fatal("upsertTXT must return an error when a foreign ownership TXT is present")
	}
	if n := countOp(prov.calls, "create") + countOp(prov.calls, "update"); n != 0 {
		t.Errorf("expected no writes, got calls: %v", prov.calls)
	}
}

func TestUpsertTXT_IgnoresForeignTXTAtOtherNames(t *testing.T) {
	// The guard only looks at the exact key being written.
	prov := &fakeProvider{}
	ctrl := newCtrl(&fakeSource{}, prov)
	foreign := externalDNSTXT("t-aaaa", "foo.example.com")
	foreign.Name = "aaaa-foo.example.com"
	current := []dns.Record{foreign}

	if err := ctrl.upsertTXT(context.Background(), ep("foo.example.com", "192.0.2.1"), current); err != nil {
		t.Fatalf("upsertTXT returned error: %v", err)
	}
	if creates := opKeys(prov.calls, "create"); len(creates) != 1 || creates[0] != "a-foo.example.com" {
		t.Errorf("expected one TXT create at a-foo.example.com, got creates: %v", creates)
	}
}

func TestDeletePair_DeletesOnlyOwnedTXT(t *testing.T) {
	prov := &fakeProvider{}
	ctrl := newCtrl(&fakeSource{}, prov)
	aRecord := aRec("a1", "foo.example.com", "192.0.2.1")
	current := []dns.Record{
		aRecord,
		externalDNSTXT("t-foreign", "foo.example.com"),
		ownedTXT("t-ours", "foo.example.com", testOwner),
	}

	if err := ctrl.deletePair(context.Background(), aRecord, current); err != nil {
		t.Fatalf("deletePair returned error: %v", err)
	}

	var deletedIDs []string
	for _, c := range prov.calls {
		if c.Op == "delete" {
			deletedIDs = append(deletedIDs, c.Record.ID)
		}
	}
	if !containsAll(deletedIDs, []string{"a1", "t-ours"}) {
		t.Errorf("expected deletes of a1 and t-ours, got %v", deletedIDs)
	}
	if containsAll(deletedIDs, []string{"t-foreign"}) {
		t.Errorf("foreign TXT must never be deleted, got %v", deletedIDs)
	}
}

func TestReconcile_ConflictMetric(t *testing.T) {
	prov := &fakeProvider{initial: []dns.Record{
		aRec("*1", "foo.example.com", "192.0.2.1"),
		aRec("*2", "foo.example.com", "192.0.2.2"),
		ownedTXT("t1", "foo.example.com", testOwner),
	}}
	newCtrl(&fakeSource{}, prov).reconcile(context.Background())

	if n := countOp(prov.calls, "delete") + countOp(prov.calls, "update"); n != 0 {
		t.Errorf("conflicted hostname must not be touched, got calls: %v", prov.calls)
	}
	if got := planChangesGauge(t, "conflict"); got != 1 {
		t.Errorf("dexd_mikrotik_plan_changes{operation=\"conflict\"} = %v, want 1", got)
	}
}

// planChangesGauge reads dexd_mikrotik_plan_changes for one operation label
// from the default registry, where the metrics package registers it.
func planChangesGauge(t *testing.T, operation string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "dexd_mikrotik_plan_changes" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "operation" && lp.GetValue() == operation {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("dexd_mikrotik_plan_changes{operation=%q} not found", operation)
	return 0
}
