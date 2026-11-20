package mikrotik

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doonga/dexd-mikrotik/internal/dns"
	"github.com/doonga/dexd-mikrotik/internal/registry"
)

// TestLive_RoundTrip exercises a real RouterOS 7 router. It is skipped unless
// MIKROTIK_INTEGRATION=1. It only ever touches the names MIKROTIK_TEST_NAME,
// "a-" + MIKROTIK_TEST_NAME and "c." + MIKROTIK_TEST_NAME.
func TestLive_RoundTrip(t *testing.T) {
	if os.Getenv("MIKROTIK_INTEGRATION") != "1" {
		t.Skip("set MIKROTIK_INTEGRATION=1 to run against a real router")
	}
	skipTLS := false
	if v := os.Getenv("MIKROTIK_SKIP_TLS_VERIFY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			t.Fatalf("MIKROTIK_SKIP_TLS_VERIFY: %v", err)
		}
		skipTLS = b
	}
	name := os.Getenv("MIKROTIK_TEST_NAME")
	if name == "" {
		name = "dexd-mikrotik-livetest.invalid"
	}
	txtName := "a-" + name
	cnameName := "c." + name
	isTestName := func(n string) bool {
		return strings.EqualFold(n, name) || strings.EqualFold(n, txtName) || strings.EqualFold(n, cnameName)
	}

	c := NewClient(Options{
		BaseURL:       os.Getenv("MIKROTIK_BASEURL"),
		Username:      os.Getenv("MIKROTIK_USERNAME"),
		Password:      os.Getenv("MIKROTIK_PASSWORD"),
		SkipTLSVerify: skipTLS,
		Timeout:       15 * time.Second,
	})
	ctx := context.Background()

	// Track created record IDs; cleanup deletes whatever is still tracked.
	created := map[string]dns.Record{}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for id, r := range created {
			if err := c.DeleteRecord(cctx, id, r.Name, r.Type); err != nil {
				t.Errorf("cleanup: delete %s (%s %s): %v", id, r.Type, r.Name, err)
			}
		}
	})

	find := func(recs []dns.Record, n, typ string) (dns.Record, bool) {
		for _, r := range recs {
			if strings.EqualFold(r.Name, n) && r.Type == typ {
				return r, true
			}
		}
		return dns.Record{}, false
	}

	res, err := c.SystemResource(ctx)
	if err != nil {
		t.Fatalf("SystemResource: %v", err)
	}
	if !strings.HasPrefix(res.Version, "7.") {
		t.Fatalf("unexpected RouterOS version %q", res.Version)
	}

	// Refuse to run if the test names already exist, so we never adopt or
	// delete a pre-existing record.
	pre, err := c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	for _, r := range pre {
		if isTestName(r.Name) {
			t.Fatalf("test name already present on router (%s %s); remove it or pick another MIKROTIK_TEST_NAME", r.Type, r.Name)
		}
	}

	// A record: create, list, update.
	a, err := c.CreateRecord(ctx, dns.Record{Name: name, Type: "A", Value: "192.0.2.123"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	if a.ID == "" {
		t.Fatal("create A returned empty ID")
	}
	created[a.ID] = dns.Record{Name: name, Type: "A"}

	recs, err := c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("list after create A: %v", err)
	}
	got, ok := find(recs, name, "A")
	if !ok {
		t.Fatal("A record not found in list")
	}
	if got.ID == "" || got.Name != strings.ToLower(name) || got.Value != "192.0.2.123" {
		t.Fatalf("unexpected A record: %+v", got)
	}

	up, err := c.UpdateRecord(ctx, dns.Record{ID: got.ID, Name: name, Type: "A", Value: "192.0.2.124"})
	if err != nil {
		t.Fatalf("update A: %v", err)
	}
	if up.ID != got.ID {
		t.Fatalf("update changed ID: %q -> %q", got.ID, up.ID)
	}
	recs, err = c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if g, ok := find(recs, name, "A"); !ok || g.ID != got.ID || g.Value != "192.0.2.124" {
		t.Fatalf("A record not updated in place: %+v (found=%v)", g, ok)
	}

	// TXT record: value must round-trip byte-identical.
	txtVal := registry.EncodeTXT("livetest", "docker/live")
	txt, err := c.CreateRecord(ctx, dns.Record{Name: txtName, Type: "TXT", Value: txtVal})
	if err != nil {
		t.Fatalf("create TXT: %v", err)
	}
	if txt.ID == "" {
		t.Fatal("create TXT returned empty ID")
	}
	created[txt.ID] = dns.Record{Name: txtName, Type: "TXT"}

	recs, err = c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("list after create TXT: %v", err)
	}
	gt, ok := find(recs, txtName, "TXT")
	if !ok {
		t.Fatal("TXT record not found in list")
	}
	if gt.ID == "" || gt.Name != strings.ToLower(txtName) {
		t.Fatalf("unexpected TXT record: %+v", gt)
	}
	if gt.Value != txtVal {
		t.Fatalf("TXT value mangled: sent %q, got %q", txtVal, gt.Value)
	}

	// CNAME record: create, list, update in place, delete.
	cn, err := c.CreateRecord(ctx, dns.Record{Name: cnameName, Type: "CNAME", Value: "target.example.com"})
	if err != nil {
		t.Fatalf("create CNAME: %v", err)
	}
	if cn.ID == "" {
		t.Fatal("create CNAME returned empty ID")
	}
	created[cn.ID] = dns.Record{Name: cnameName, Type: "CNAME"}

	recs, err = c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("list after create CNAME: %v", err)
	}
	gc, ok := find(recs, cnameName, "CNAME")
	if !ok {
		t.Fatal("CNAME record not found in list")
	}
	if gc.ID != cn.ID || gc.Name != strings.ToLower(cnameName) || gc.Value != "target.example.com" {
		t.Fatalf("unexpected CNAME record: %+v", gc)
	}

	if _, err := c.UpdateRecord(ctx, dns.Record{ID: gc.ID, Name: cnameName, Type: "CNAME", Value: "target2.example.com"}); err != nil {
		t.Fatalf("update CNAME: %v", err)
	}
	recs, err = c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("list after update CNAME: %v", err)
	}
	if g, ok := find(recs, cnameName, "CNAME"); !ok || g.ID != gc.ID || g.Value != "target2.example.com" {
		t.Fatalf("CNAME record not updated in place: %+v (found=%v)", g, ok)
	}

	// Delete everything created and confirm it is gone.
	for id, r := range created {
		if err := c.DeleteRecord(ctx, id, r.Name, r.Type); err != nil {
			t.Fatalf("delete %s %s: %v", r.Type, r.Name, err)
		}
		delete(created, id)
	}
	recs, err = c.ListRecords(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	for _, r := range recs {
		if isTestName(r.Name) {
			t.Fatalf("record still present after delete: %+v", r)
		}
	}
}
