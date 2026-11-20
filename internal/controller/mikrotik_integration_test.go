package controller

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doonga/dexd-mikrotik/internal/config"
	"github.com/doonga/dexd-mikrotik/internal/provider/mikrotik"
	"github.com/doonga/dexd-mikrotik/internal/provider/mikrotik/mikrotiktest"
	"github.com/doonga/dexd-mikrotik/internal/registry"
	"github.com/doonga/dexd-mikrotik/internal/source"
)

func newSimProvider(t *testing.T, sim *mikrotiktest.Sim) *mikrotik.Client {
	t.Helper()
	return mikrotik.NewClient(mikrotik.Options{
		BaseURL:       sim.URL,
		Username:      sim.Username(),
		Password:      sim.Password(),
		SkipTLSVerify: true,
		DefaultTTL:    300,
	})
}

func reconcileOnce(t *testing.T, sim *mikrotiktest.Sim, endpoints ...*source.Endpoint) {
	t.Helper()
	src := &fakeSource{endpoints: endpoints}
	New(src, newSimProvider(t, sim), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())
}

// finalState summarises simulator entries as "TYPE name=value" lines, sorted.
func finalState(sim *mikrotiktest.Sim) []string {
	entries := sim.Entries()
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		value := e["address"]
		if e["type"] == "CNAME" {
			value = e["cname"]
		}
		if e["type"] == "TXT" {
			value = e["text"]
		}
		out = append(out, e["type"]+" "+e["name"]+"="+value)
	}
	sort.Strings(out)
	return out
}

func assertState(t *testing.T, sim *mikrotiktest.Sim, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := finalState(sim)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("simulator entries:\n got %q\nwant %q", got, want)
	}
}

func txtLine(recordType, host string) string {
	return "TXT " + registry.TXTKey("", recordType, host) + "=" + registry.EncodeTXT(testOwner, "docker/"+host)
}

func TestReconcileWithMikrotik_CreatesPair(t *testing.T) {
	sim := mikrotiktest.New(t)

	reconcileOnce(t, sim, ep("foo.example.com", "192.0.2.10"))

	assertState(t, sim,
		"A foo.example.com=192.0.2.10",
		txtLine("A", "foo.example.com"),
	)
	// The ownership TXT must be created before the record it guards.
	var order []string
	for _, r := range sim.Requests() {
		if r.Method == http.MethodPut {
			order = append(order, r.Body["name"])
		}
	}
	if len(order) != 2 || order[0] != registry.TXTKey("", "A", "foo.example.com") || order[1] != "foo.example.com" {
		t.Fatalf("create order = %v, want TXT then A", order)
	}
}

func TestReconcileWithMikrotik_UpdatesValue(t *testing.T) {
	sim := mikrotiktest.New(t)
	reconcileOnce(t, sim, ep("foo.example.com", "192.0.2.10"))

	reconcileOnce(t, sim, ep("foo.example.com", "192.0.2.11"))

	assertState(t, sim,
		"A foo.example.com=192.0.2.11",
		txtLine("A", "foo.example.com"),
	)
}

func TestReconcileWithMikrotik_ReplacesAWithCNAME(t *testing.T) {
	sim := mikrotiktest.New(t)
	reconcileOnce(t, sim, ep("foo.example.com", "192.0.2.10"))

	reconcileOnce(t, sim, epType("foo.example.com", "target.example.com", "CNAME"))

	assertState(t, sim,
		"CNAME foo.example.com=target.example.com",
		txtLine("CNAME", "foo.example.com"),
	)
}

func TestReconcileWithMikrotik_DeletesPair(t *testing.T) {
	sim := mikrotiktest.New(t)
	reconcileOnce(t, sim, ep("foo.example.com", "192.0.2.10"))

	reconcileOnce(t, sim)

	assertState(t, sim)
}

func TestReconcileWithMikrotik_SkipsWildcard(t *testing.T) {
	sim := mikrotiktest.New(t)

	reconcileOnce(t, sim,
		ep("*.s3.example.com", "192.0.2.20"),
		ep("foo.example.com", "192.0.2.10"),
	)

	// The wildcard is skipped; the rest of the reconcile still applies.
	assertState(t, sim,
		"A foo.example.com=192.0.2.10",
		txtLine("A", "foo.example.com"),
	)
}
