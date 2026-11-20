package plan

import (
	"slices"
	"testing"

	"github.com/doonga/dexd-mikrotik/internal/dns"
	"github.com/doonga/dexd-mikrotik/internal/registry"
	"github.com/doonga/dexd-mikrotik/internal/source"
)

const ownerID = "us"

// helpers for building inputs

func endpoint(name, target string) *source.Endpoint {
	return endpointType(name, target, "A")
}

func endpointType(name, target, recordType string) *source.Endpoint {
	return &source.Endpoint{
		DNSName:    name,
		Target:     target,
		RecordType: recordType,
		OwnerID:    ownerID,
		Resource:   "docker/" + name,
	}
}

func aRecord(id, key, value string) dns.Record {
	return dns.Record{ID: id, Name: key, Type: "A", Value: value}
}

func cnameRecord(id, key, value string) dns.Record {
	return dns.Record{ID: id, Name: key, Type: "CNAME", Value: value}
}

func ownedTXT(id, hostname, owner string) dns.Record {
	return ownedTXTType("", id, "A", hostname, owner)
}

func ownedTXTWithPrefix(prefix, id, hostname, owner string) dns.Record {
	return ownedTXTType(prefix, id, "A", hostname, owner)
}

func ownedTXTType(prefix, id, recordType, hostname, owner string) dns.Record {
	return dns.Record{
		ID:    id,
		Name:  registry.TXTKey(prefix, recordType, hostname),
		Type:  "TXT",
		Value: registry.EncodeTXT(owner, "docker/"+hostname),
	}
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name        string
		desired     []*source.Endpoint
		current     []dns.Record
		wantCreate  []string
		wantUpdate  []string
		wantReplace []string
		wantDelete  []string
		wantOrphan  []string
	}{
		{
			name: "empty desired and current",
		},
		{
			name:       "create new record",
			desired:    []*source.Endpoint{endpoint("foo.example.com", "10.0.0.1")},
			wantCreate: []string{"A:foo.example.com"},
		},
		{
			name:    "no-op when desired matches owned current",
			desired: []*source.Endpoint{endpoint("foo.example.com", "10.0.0.1")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", ownerID),
			},
		},
		{
			name:    "update when desired target differs and we own the record",
			desired: []*source.Endpoint{endpoint("foo.example.com", "10.0.0.2")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", ownerID),
			},
			wantUpdate: []string{"A:foo.example.com"},
		},
		{
			name:    "no update when target differs but no ownership TXT exists",
			desired: []*source.Endpoint{endpoint("foo.example.com", "10.0.0.2")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				// no TXT
			},
		},
		{
			name:    "no update when TXT belongs to a different owner",
			desired: []*source.Endpoint{endpoint("foo.example.com", "10.0.0.2")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", "someone-else"),
			},
		},
		{
			name: "delete owned record no longer desired",
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", ownerID),
			},
			wantDelete: []string{"A:foo.example.com"},
		},
		{
			name: "do NOT delete unowned record (no companion TXT)",
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				// no TXT — created by someone external
			},
		},
		{
			name: "do NOT delete record owned by someone else",
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", "someone-else"),
			},
		},
		{
			name:       "create CNAME from empty current",
			desired:    []*source.Endpoint{endpointType("foo.example.com", "traefik.example.com", "CNAME")},
			wantCreate: []string{"CNAME:foo.example.com"},
		},
		{
			name:    "update owned CNAME when target changes",
			desired: []*source.Endpoint{endpointType("foo.example.com", "new.example.com", "CNAME")},
			current: []dns.Record{
				cnameRecord("c1", "foo.example.com", "old.example.com"),
				ownedTXTType("", "t1", "CNAME", "foo.example.com", ownerID),
			},
			wantUpdate: []string{"CNAME:foo.example.com"},
		},
		{
			name: "delete owned CNAME no longer desired",
			current: []dns.Record{
				cnameRecord("c1", "foo.example.com", "old.example.com"),
				ownedTXTType("", "t1", "CNAME", "foo.example.com", ownerID),
			},
			wantDelete: []string{"CNAME:foo.example.com"},
		},
		{
			name: "A and CNAME for different hostnames coexist",
			desired: []*source.Endpoint{
				endpoint("a.example.com", "10.0.0.1"),
				endpointType("c.example.com", "target.example.com", "CNAME"),
			},
			current: []dns.Record{
				aRecord("a1", "a.example.com", "10.0.0.1"),
				ownedTXT("t1", "a.example.com", ownerID),
				cnameRecord("c1", "c.example.com", "target.example.com"),
				ownedTXTType("", "t2", "CNAME", "c.example.com", ownerID),
			},
		},
		{
			name:    "record type flip replaces old A with new CNAME",
			desired: []*source.Endpoint{endpointType("foo.example.com", "bar.example.com", "CNAME")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				ownedTXT("t1", "foo.example.com", ownerID),
			},
			wantReplace: []string{"A:foo.example.com->CNAME:foo.example.com"},
		},
		{
			name:    "record type flip does not claim unowned old record",
			desired: []*source.Endpoint{endpointType("foo.example.com", "bar.example.com", "CNAME")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
			},
		},
		{
			name: "collision: two desired endpoints for the same hostname, only one create",
			desired: []*source.Endpoint{
				endpointType("foo.example.com", "10.0.0.1", "A"),
				endpointType("foo.example.com", "10.0.0.2", "A"),
			},
			wantCreate: []string{"A:foo.example.com"},
		},
		{
			name:    "unowned existing record: warn but no create, update, or delete",
			desired: []*source.Endpoint{endpoint("foo.example.com", "10.0.0.1")},
			current: []dns.Record{
				aRecord("a1", "foo.example.com", "10.0.0.1"),
				// no TXT — not managed by us
			},
		},
		{
			name:    "orphan TXT with desired hostname: create A, no orphan",
			desired: []*source.Endpoint{endpoint("foo.example.com", "10.0.0.1")},
			current: []dns.Record{
				ownedTXT("t1", "foo.example.com", ownerID), // TXT present, A missing
			},
			wantCreate: []string{"A:foo.example.com"},
		},
		{
			name: "orphan TXT with no desired hostname: added to OrphanTXT",
			current: []dns.Record{
				ownedTXT("t1", "foo.example.com", ownerID), // TXT present, A missing, not desired
			},
			wantOrphan: []string{"TXT:a-foo.example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compute(tt.desired, tt.current, ownerID, "")

			gotCreate := endpointKeys(got.Create)
			gotUpdate := endpointKeys(got.Update)
			gotReplace := replaceKeys(got.Replace)
			gotDelete := recordKeys(got.Delete)
			gotOrphan := recordKeys(got.OrphanTXT)

			if !sameSet(gotCreate, tt.wantCreate) {
				t.Errorf("Create = %v, want %v", gotCreate, tt.wantCreate)
			}
			if !sameSet(gotUpdate, tt.wantUpdate) {
				t.Errorf("Update = %v, want %v", gotUpdate, tt.wantUpdate)
			}
			if !sameSet(gotReplace, tt.wantReplace) {
				t.Errorf("Replace = %v, want %v", gotReplace, tt.wantReplace)
			}
			if !sameSet(gotDelete, tt.wantDelete) {
				t.Errorf("Delete = %v, want %v", gotDelete, tt.wantDelete)
			}
			if !sameSet(gotOrphan, tt.wantOrphan) {
				t.Errorf("OrphanTXT = %v, want %v", gotOrphan, tt.wantOrphan)
			}
		})
	}
}

func TestDesiredRecordCounts(t *testing.T) {
	desired := []*source.Endpoint{
		endpoint("a.example.com", "10.0.0.1"),
		endpoint("a.example.com", "10.0.0.2"), // collision; count once
		endpointType("c.example.com", "target.example.com", "CNAME"),
	}

	got := DesiredRecordCounts(desired)
	if got["A"] != 1 || got["CNAME"] != 1 {
		t.Errorf("DesiredRecordCounts = %v, want A=1 CNAME=1", got)
	}
}

func TestCompute_WithTXTPrefix(t *testing.T) {
	const prefix = "userprefix."
	desired := []*source.Endpoint{endpoint("foo.example.com", "10.0.0.2")}
	current := []dns.Record{
		aRecord("a1", "foo.example.com", "10.0.0.1"),
		ownedTXTWithPrefix(prefix, "t1", "foo.example.com", ownerID),
	}

	got := Compute(desired, current, ownerID, prefix)

	if len(got.Create) != 0 || len(got.Delete) != 0 {
		t.Errorf("expected no create/delete, got create=%v delete=%v", got.Create, got.Delete)
	}
	if len(got.Update) != 1 || got.Update[0].DNSName != "foo.example.com" {
		t.Errorf("expected update for foo.example.com, got %v", got.Update)
	}
}

func foreignTXT(id, hostname string) dns.Record {
	return dns.Record{
		ID:    id,
		Name:  registry.TXTKey("", "A", hostname),
		Type:  "TXT",
		Value: "heritage=external-dns,external-dns/owner=default,external-dns/resource=ingress/x/y",
	}
}

func assertNoChanges(t *testing.T, got Changes) {
	t.Helper()
	if len(got.Create)+len(got.Update)+len(got.Replace)+len(got.Delete)+len(got.OrphanTXT) != 0 {
		t.Errorf("expected no changes, got create=%v update=%v replace=%v delete=%v orphan=%v",
			endpointKeys(got.Create), endpointKeys(got.Update), replaceKeys(got.Replace),
			recordKeys(got.Delete), recordKeys(got.OrphanTXT))
	}
}

func assertConflicts(t *testing.T, got Changes, want ...string) {
	t.Helper()
	if !slices.Equal(got.Conflicts, want) {
		t.Errorf("Conflicts = %v, want %v", got.Conflicts, want)
	}
}

func recordIDs(rs []dns.Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestCompute_ForeignTXTAtSameKeyIsConflict(t *testing.T) {
	// external-dns would believe it owns the A at foo, so we must not touch it.
	desired := []*source.Endpoint{endpoint("foo.example.com", "192.0.2.2")}
	current := []dns.Record{
		ownedTXT("t-ours", "foo.example.com", ownerID),
		foreignTXT("t-foreign", "foo.example.com"),
		aRecord("a1", "foo.example.com", "192.0.2.1"),
	}

	got := Compute(desired, current, ownerID, "")

	assertNoChanges(t, got)
	assertConflicts(t, got, "foo.example.com")
}

func TestCompute_OtherDexdOwnerIsConflict(t *testing.T) {
	desired := []*source.Endpoint{endpoint("foo.example.com", "192.0.2.1")}
	current := []dns.Record{ownedTXT("t-other", "foo.example.com", "other")}

	got := Compute(desired, current, ownerID, "")

	assertNoChanges(t, got)
	assertConflicts(t, got, "foo.example.com")
}

func TestCompute_DuplicateAtUnrelatedHostnameIgnored(t *testing.T) {
	current := []dns.Record{
		aRecord("*1", "rr.example.com", "192.0.2.1"),
		aRecord("*2", "rr.example.com", "192.0.2.2"),
	}

	got := Compute(nil, current, ownerID, "")

	assertNoChanges(t, got)
	assertConflicts(t, got)
}

func TestCompute_ForeignTXTAtUnrelatedHostnameIgnored(t *testing.T) {
	current := []dns.Record{
		foreignTXT("t-foreign", "ext.example.com"),
		aRecord("a1", "ext.example.com", "192.0.2.1"),
	}

	got := Compute(nil, current, ownerID, "")

	assertNoChanges(t, got)
	assertConflicts(t, got)
}

func externalDNSTXTAt(id, name string) dns.Record {
	return dns.Record{
		ID:    id,
		Name:  name,
		Type:  "TXT",
		Value: "heritage=external-dns,external-dns/owner=default,external-dns/resource=ingress/x/y",
	}
}

func TestCompute_ForeignAAAATXTIgnored(t *testing.T) {
	desired := []*source.Endpoint{endpoint("foo.example.com", "192.0.2.1")}
	current := []dns.Record{externalDNSTXTAt("t-foreign", "aaaa-foo.example.com")}

	got := Compute(desired, current, ownerID, "")

	if keys := endpointKeys(got.Create); !slices.Equal(keys, []string{"A:foo.example.com"}) {
		t.Errorf("Create = %v, want [A:foo.example.com]", keys)
	}
	assertConflicts(t, got)
}

func TestCompute_ForeignTXTAtDashedHostnameIgnored(t *testing.T) {
	// Old-format external-dns TXT at the hostname itself: my-app.example.com
	// parses as type "MY", host app.example.com, which is not an ownership key.
	desired := []*source.Endpoint{endpoint("app.example.com", "192.0.2.1")}
	current := []dns.Record{externalDNSTXTAt("t-foreign", "my-app.example.com")}

	got := Compute(desired, current, ownerID, "")

	if keys := endpointKeys(got.Create); !slices.Equal(keys, []string{"A:app.example.com"}) {
		t.Errorf("Create = %v, want [A:app.example.com]", keys)
	}
	assertConflicts(t, got)
}

func TestCompute_NonOwnershipTXTIgnored(t *testing.T) {
	desired := []*source.Endpoint{endpoint("foo.example.com", "192.0.2.1")}
	current := []dns.Record{{ID: "t-spf", Name: "a-foo.example.com", Type: "TXT", Value: "v=spf1 -all"}}

	got := Compute(desired, current, ownerID, "")

	if keys := endpointKeys(got.Create); !slices.Equal(keys, []string{"A:foo.example.com"}) {
		t.Errorf("Create = %v, want [A:foo.example.com]", keys)
	}
	assertConflicts(t, got)
}

func TestCompute_ForeignTXTOnly_IsUnowned(t *testing.T) {
	desired := []*source.Endpoint{endpoint("foo.example.com", "192.0.2.2")}
	current := []dns.Record{
		foreignTXT("t-foreign", "foo.example.com"),
		aRecord("a1", "foo.example.com", "192.0.2.1"),
	}

	got := Compute(desired, current, ownerID, "")

	assertNoChanges(t, got)
	assertConflicts(t, got, "foo.example.com")
}

func TestCompute_DuplicateAEntriesConflict(t *testing.T) {
	current := []dns.Record{
		aRecord("*1", "foo.example.com", "192.0.2.1"),
		aRecord("*2", "foo.example.com", "192.0.2.2"),
		ownedTXT("t1", "foo.example.com", ownerID),
	}

	got := Compute(nil, current, ownerID, "")

	if len(got.Delete) != 0 {
		t.Errorf("Delete = %v, want empty", recordIDs(got.Delete))
	}
	if len(got.OrphanTXT) != 0 {
		t.Errorf("OrphanTXT = %v, want empty", recordIDs(got.OrphanTXT))
	}
	assertConflicts(t, got, "foo.example.com")
}

func TestCompute_ACnameMixConflict(t *testing.T) {
	desired := []*source.Endpoint{endpointType("foo.example.com", "bar.example.com", "CNAME")}
	current := []dns.Record{
		aRecord("a1", "foo.example.com", "192.0.2.1"),
		ownedTXT("t1", "foo.example.com", ownerID),
		cnameRecord("c1", "foo.example.com", "old.example.com"),
		ownedTXTType("", "t2", "CNAME", "foo.example.com", ownerID),
	}

	got := Compute(desired, current, ownerID, "")

	if len(got.Replace) != 0 {
		t.Errorf("Replace = %v, want empty", replaceKeys(got.Replace))
	}
	assertNoChanges(t, got)
	assertConflicts(t, got, "foo.example.com")
}

func TestCompute_CaseInsensitiveNames(t *testing.T) {
	desired := []*source.Endpoint{endpoint("Foo.Example.COM", "192.0.2.1")}
	current := []dns.Record{
		aRecord("a1", "foo.example.com", "192.0.2.1"),
		ownedTXT("t1", "foo.example.com", ownerID),
	}

	got := Compute(desired, current, ownerID, "")

	assertNoChanges(t, got)
	assertConflicts(t, got)
}

func TestCompute_OrphanOnlyOwnedTXT(t *testing.T) {
	current := []dns.Record{
		ownedTXT("t-ours", "gone.example.com", ownerID),
		foreignTXT("t-foreign", "gone.example.com"),
	}

	got := Compute(nil, current, ownerID, "")

	if ids := recordIDs(got.OrphanTXT); !slices.Equal(ids, []string{"t-ours"}) {
		t.Errorf("OrphanTXT IDs = %v, want [t-ours]", ids)
	}
	if len(got.Delete) != 0 {
		t.Errorf("Delete = %v, want empty", recordIDs(got.Delete))
	}
	// The foreign TXT still marks the hostname as a conflict; only our own
	// orphan TXT is cleaned up.
	assertConflicts(t, got, "gone.example.com")
}

func endpointKeys(eps []*source.Endpoint) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.RecordType + ":" + e.DNSName
	}
	return out
}

func recordKeys(rs []dns.Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Type + ":" + r.Name
	}
	return out
}

func replaceKeys(replacements []Replace) []string {
	out := make([]string, len(replacements))
	for i, r := range replacements {
		out[i] = r.Old.Type + ":" + r.Old.Name + "->" + r.Desired.RecordType + ":" + r.Desired.DNSName
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestCompute_DesiredAAndCNAMEConflict(t *testing.T) {
	desired := []*source.Endpoint{
		endpoint("foo.example.com", "192.0.2.1"),
		endpointType("foo.example.com", "target.example.com", "CNAME"),
	}

	t.Run("no existing records", func(t *testing.T) {
		got := Compute(desired, nil, ownerID, "")

		assertNoChanges(t, got)
		assertConflicts(t, got, "foo.example.com")
	})

	t.Run("owned A exists", func(t *testing.T) {
		current := []dns.Record{
			aRecord("a1", "foo.example.com", "192.0.2.9"),
			ownedTXT("t1", "foo.example.com", ownerID),
		}

		got := Compute(desired, current, ownerID, "")

		assertNoChanges(t, got)
		assertConflicts(t, got, "foo.example.com")
	})

	t.Run("owned CNAME exists", func(t *testing.T) {
		current := []dns.Record{
			cnameRecord("c1", "foo.example.com", "old.example.com"),
			ownedTXTType("", "t1", "CNAME", "foo.example.com", ownerID),
		}

		got := Compute(desired, current, ownerID, "")

		assertNoChanges(t, got)
		assertConflicts(t, got, "foo.example.com")
	})
}
