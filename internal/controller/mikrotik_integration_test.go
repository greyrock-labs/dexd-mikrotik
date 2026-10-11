package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/greyrock-labs/dexd-mikrotik/internal/config"
	"github.com/greyrock-labs/dexd-mikrotik/internal/provider/mikrotik"
	"github.com/greyrock-labs/dexd-mikrotik/internal/registry"
	"github.com/greyrock-labs/dexd-mikrotik/internal/source"
)

const routerOSBase = "/rest/ip/dns/static"

// routerOSHTTPCall records one request. Key is the entry name, or the
// regexp for wildcard entries, so assertions can refer to either.
type routerOSHTTPCall struct {
	Method string
	Path   string
	Key    string
	Body   map[string]string
}

// controllerRouterOSAPI is a fake RouterOS /rest/ip/dns/static endpoint.
type controllerRouterOSAPI struct {
	t          *testing.T
	server     *httptest.Server
	records    map[string]map[string]string
	nextID     int
	calls      []routerOSHTTPCall
	failCreate string
}

func newControllerRouterOSAPI(t *testing.T, records ...map[string]string) *controllerRouterOSAPI {
	t.Helper()
	api := &controllerRouterOSAPI{
		t:       t,
		records: make(map[string]map[string]string),
	}
	for _, r := range records {
		api.records[r[".id"]] = r
	}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)
	return api
}

func (api *controllerRouterOSAPI) provider() *mikrotik.Client {
	client, err := mikrotik.NewClient(api.server.URL, "dexd", "secret", false, "", false, 300)
	if err != nil {
		api.t.Fatalf("NewClient: %v", err)
	}
	return client
}

func (api *controllerRouterOSAPI) handle(w http.ResponseWriter, r *http.Request) {
	call := routerOSHTTPCall{Method: r.Method, Path: r.URL.Path}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&call.Body)
	}
	call.Key = entryKey(call.Body)

	switch {
	case r.Method == http.MethodGet && r.URL.Path == routerOSBase:
		records := make([]map[string]string, 0, len(api.records))
		for _, rec := range api.records {
			records = append(records, rec)
		}
		api.calls = append(api.calls, call)
		_ = json.NewEncoder(w).Encode(records)
	case r.Method == http.MethodPut && r.URL.Path == routerOSBase:
		api.calls = append(api.calls, call)
		api.create(w, call.Body)
	case strings.HasPrefix(r.URL.Path, routerOSBase+"/"):
		id := strings.TrimPrefix(r.URL.Path, routerOSBase+"/")
		if rec, ok := api.records[id]; ok && call.Key == "" {
			call.Key = entryKey(rec)
		}
		api.calls = append(api.calls, call)
		switch r.Method {
		case http.MethodPatch:
			api.update(w, id, call.Body)
		case http.MethodDelete:
			delete(api.records, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (api *controllerRouterOSAPI) create(w http.ResponseWriter, body map[string]string) {
	if api.failCreate == entryKey(body) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 400, "message": "Bad Request", "detail": "injected create failure"})
		return
	}
	api.nextID++
	record := map[string]string{".id": fmt.Sprintf("*%X", api.nextID)}
	for k, v := range body {
		record[k] = v
	}
	api.records[record[".id"]] = record
	_ = json.NewEncoder(w).Encode(record)
}

func (api *controllerRouterOSAPI) update(w http.ResponseWriter, id string, body map[string]string) {
	record, ok := api.records[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	for k, v := range body {
		record[k] = v
	}
	_ = json.NewEncoder(w).Encode(record)
}

func entryKey(body map[string]string) string {
	if body["name"] != "" {
		return body["name"]
	}
	return body["regexp"]
}

func TestReconcileWithMikroTikClient_CreatesTXTBeforeRecord(t *testing.T) {
	api := newControllerRouterOSAPI(t)
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}

	New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())

	assertHTTPBefore(t, api.calls, http.MethodPut, "a-foo.example.com", http.MethodPut, "foo.example.com")
	assertHTTPBody(t, api.calls, "foo.example.com", map[string]string{"type": "A", "address": "10.0.0.1", "ttl": "300s"})
	assertHTTPBody(t, api.calls, "a-foo.example.com", map[string]string{"type": "TXT", "text": registry.EncodeTXT(testOwner, "docker/foo.example.com")})
}

func TestReconcileWithMikroTikClient_IsIdempotent(t *testing.T) {
	api := newControllerRouterOSAPI(t)
	src := &fakeSource{endpoints: []*source.Endpoint{
		ep("foo.example.com", "10.0.0.1"),
		epType("bar.example.com", "target.example.com", "CNAME"),
		ep("*.foo.example.com", "10.0.0.1"),
	}}
	ctrl := New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour)

	ctrl.reconcile(context.Background())
	api.calls = nil
	ctrl.reconcile(context.Background())

	for _, call := range api.calls {
		if call.Method != http.MethodGet {
			t.Fatalf("second reconcile mutated records: %+v", api.calls)
		}
	}
}

func TestReconcileWithMikroTikClient_UpdatesTargetInPlace(t *testing.T) {
	api := newControllerRouterOSAPI(t,
		map[string]string{".id": "*1", "name": "foo.example.com", "address": "10.0.0.1", "ttl": "1d"},
		map[string]string{
			".id":  "*2",
			"name": registry.TXTKey("", "A", "foo.example.com"),
			"type": "TXT",
			"text": registry.EncodeTXT(testOwner, "docker/foo.example.com"),
		},
	)
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.2")}}

	New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())

	if got := api.records["*1"]["address"]; got != "10.0.0.2" {
		t.Fatalf("address = %q, want 10.0.0.2; calls: %+v", got, api.calls)
	}
	if idx := httpCallIndex(api.calls, http.MethodPatch, "*1"); idx < 0 {
		t.Fatalf("missing PATCH for *1 in %+v", api.calls)
	}
}

func TestReconcileWithMikroTikClient_ReplacesTypeBeforeCreate(t *testing.T) {
	api := newControllerRouterOSAPI(t,
		map[string]string{".id": "*1", "name": "foo.example.com", "type": "A", "address": "10.0.0.1"},
		map[string]string{
			".id":  "*2",
			"name": registry.TXTKey("", "A", "foo.example.com"),
			"type": "TXT",
			"text": registry.EncodeTXT(testOwner, "docker/foo.example.com"),
		},
	)
	src := &fakeSource{endpoints: []*source.Endpoint{epType("foo.example.com", "target.example.com", "CNAME")}}

	New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())

	assertHTTPBefore(t, api.calls, http.MethodDelete, "foo.example.com", http.MethodPut, "cname-foo.example.com")
	assertHTTPBefore(t, api.calls, http.MethodDelete, "a-foo.example.com", http.MethodPut, "cname-foo.example.com")
	assertHTTPBefore(t, api.calls, http.MethodPut, "cname-foo.example.com", http.MethodPut, "foo.example.com")
	assertHTTPBody(t, api.calls, "foo.example.com", map[string]string{"type": "CNAME", "cname": "target.example.com", "ttl": "300s"})
}

func TestReconcileWithMikroTikClient_TXTFailurePreventsRecordCreate(t *testing.T) {
	api := newControllerRouterOSAPI(t)
	api.failCreate = "a-foo.example.com"
	src := &fakeSource{endpoints: []*source.Endpoint{ep("foo.example.com", "10.0.0.1")}}

	New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())

	if idx := httpCallIndex(api.calls, http.MethodPut, "foo.example.com"); idx >= 0 {
		t.Fatalf("A record was created after TXT create failure, calls: %+v", api.calls)
	}
}

func TestReconcileWithMikroTikClient_CreatesWildcardsAsRegexp(t *testing.T) {
	api := newControllerRouterOSAPI(t)
	src := &fakeSource{endpoints: []*source.Endpoint{
		ep("foo.example.com", "10.0.0.1"),
		ep("*.foo.example.com", "10.0.0.1"),
		epType("*.bar.example.com", "target.example.com", "CNAME"),
	}}

	New(src, api.provider(), testOwner, "leo.", config.PolicySync, time.Hour).reconcile(context.Background())

	assertHTTPBody(t, api.calls, `^.+\.foo\.example\.com$`, map[string]string{"type": "A", "address": "10.0.0.1"})
	assertHTTPBody(t, api.calls, `^.+\.bar\.example\.com$`, map[string]string{"type": "CNAME", "cname": "target.example.com"})
	if idx := httpCallIndex(api.calls, http.MethodPut, "leo.a-wildcard-dexd.foo.example.com"); idx < 0 {
		t.Fatalf("wildcard A TXT was not created with encoded wildcard, calls: %+v", api.calls)
	}
	if idx := httpCallIndex(api.calls, http.MethodPut, "leo.cname-wildcard-dexd.bar.example.com"); idx < 0 {
		t.Fatalf("wildcard CNAME TXT was not created with encoded wildcard, calls: %+v", api.calls)
	}
	if idx := httpCallIndex(api.calls, http.MethodPut, "foo.example.com"); idx < 0 {
		t.Fatalf("non-wildcard A was not created, calls: %+v", api.calls)
	}
	for _, call := range api.calls {
		if strings.Contains(call.Body["name"], "*") {
			t.Fatalf("entry name contains a raw asterisk: %+v", call)
		}
	}
}

func TestReconcileWithMikroTikClient_DeletesWildcardRegexp(t *testing.T) {
	api := newControllerRouterOSAPI(t,
		map[string]string{".id": "*1", "regexp": `^.+\.foo\.example\.com$`, "address": "10.0.0.1"},
		map[string]string{
			".id":  "*2",
			"name": registry.TXTKey("", "A", "*.foo.example.com"),
			"type": "TXT",
			"text": registry.EncodeTXT(testOwner, "docker/foo"),
		},
	)
	src := &fakeSource{}

	New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())

	if len(api.records) != 0 {
		t.Fatalf("records left after sync delete: %+v", api.records)
	}
}

func TestReconcileWithMikroTikClient_IgnoresForeignEntries(t *testing.T) {
	api := newControllerRouterOSAPI(t,
		map[string]string{".id": "*1", "regexp": `.*\.ads\.example$`, "type": "NXDOMAIN"},
		map[string]string{".id": "*2", "name": "router.lan", "address": "192.168.88.1"},
		map[string]string{".id": "*3", "name": "lan", "type": "FWD", "forward-to": "10.0.0.53"},
	)
	src := &fakeSource{}

	New(src, api.provider(), testOwner, "", config.PolicySync, time.Hour).reconcile(context.Background())

	if len(api.records) != 3 {
		t.Fatalf("foreign entries were modified: %+v", api.records)
	}
	for _, call := range api.calls {
		if call.Method != http.MethodGet {
			t.Fatalf("unexpected mutation: %+v", api.calls)
		}
	}
}

func assertHTTPBefore(t *testing.T, calls []routerOSHTTPCall, firstMethod, firstKey, secondMethod, secondKey string) {
	t.Helper()
	first := httpCallIndex(calls, firstMethod, firstKey)
	second := httpCallIndex(calls, secondMethod, secondKey)
	if first < 0 {
		t.Fatalf("missing %s call for %s in %+v", firstMethod, firstKey, calls)
	}
	if second < 0 {
		t.Fatalf("missing %s call for %s in %+v", secondMethod, secondKey, calls)
	}
	if first >= second {
		t.Fatalf("expected %s %s before %s %s, got %+v", firstMethod, firstKey, secondMethod, secondKey, calls)
	}
}

// assertHTTPBody checks the PUT body for key contains want and nothing else
// besides name/regexp. An absent "ttl" in want means ttl must be omitted.
func assertHTTPBody(t *testing.T, calls []routerOSHTTPCall, key string, want map[string]string) {
	t.Helper()
	idx := httpCallIndex(calls, http.MethodPut, key)
	if idx < 0 {
		t.Fatalf("missing PUT for %s in %+v", key, calls)
	}
	got := map[string]string{}
	for k, v := range calls[idx].Body {
		if k != "name" && k != "regexp" {
			got[k] = v
		}
	}
	if _, ok := want["ttl"]; !ok {
		delete(got, "ttl")
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("PUT body for %s = %v, want %v", key, got, want)
	}
}

// httpCallIndex finds a call by entry key, or by entry ID for PATCH/DELETE.
func httpCallIndex(calls []routerOSHTTPCall, method, key string) int {
	for i, call := range calls {
		if call.Method != method {
			continue
		}
		if call.Key == key || call.Path == routerOSBase+"/"+key {
			return i
		}
	}
	return -1
}
