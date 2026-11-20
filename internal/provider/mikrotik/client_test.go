package mikrotik

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doonga/dexd-mikrotik/internal/dns"
	"github.com/doonga/dexd-mikrotik/internal/provider/mikrotik/mikrotiktest"
)

func newTestClient(t *testing.T, mut func(*Options)) (*Client, *mikrotiktest.Sim) {
	t.Helper()
	sim := mikrotiktest.New(t)
	o := Options{BaseURL: sim.URL, Username: sim.Username(), Password: sim.Password(), SkipTLSVerify: true}
	if mut != nil {
		mut(&o)
	}
	return NewClient(o), sim
}

func lastRequest(t *testing.T, sim *mikrotiktest.Sim) mikrotiktest.Request {
	t.Helper()
	reqs := sim.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	return reqs[len(reqs)-1]
}

func TestListRecords_FiltersAndMaps(t *testing.T) {
	c, sim := newTestClient(t, nil)
	sim.Seed(
		map[string]string{"name": "Foo.Example.com", "address": "192.0.2.1", "ttl": "1d"},
		map[string]string{"name": "bar.example.com", "type": "CNAME", "cname": "foo.example.com", "ttl": "5m30s"},
		map[string]string{"name": "a-foo.example.com", "type": "TXT", "text": "heritage=x,y=z", "ttl": ""},
		map[string]string{"regexp": ".*\\.ads\\..*", "address": "0.0.0.0"},
		map[string]string{"name": "v6.example.com", "type": "AAAA", "address": "2001:db8::1"},
		map[string]string{"name": "fwd.example.com", "type": "FWD"},
		map[string]string{"name": "dyn.example.com", "address": "192.0.2.9", "dynamic": "true", "disabled": "true", "ttl": "1w2d"},
		map[string]string{"name": "named.example.com", "regexp": ".*\\.named\\.example\\.com", "address": "192.0.2.8"},
	)
	got, err := c.ListRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []dns.Record{
		{ID: "*1", Name: "foo.example.com", Type: "A", Value: "192.0.2.1"},
		{ID: "*2", Name: "bar.example.com", Type: "CNAME", Value: "foo.example.com"},
		{ID: "*3", Name: "a-foo.example.com", Type: "TXT", Value: "heritage=x,y=z"},
		{ID: "*7", Name: "dyn.example.com", Type: "A", Value: "192.0.2.9"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCreateRecord_A_SendsTypeCommentTTL(t *testing.T) {
	c, sim := newTestClient(t, func(o *Options) { o.DefaultTTL = 300 })
	got, err := c.CreateRecord(context.Background(), dns.Record{Name: "foo.example.com", Type: "A", Value: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Error("expected non-empty ID")
	}
	req := lastRequest(t, sim)
	if req.Method != http.MethodPut || req.Path != "/rest/ip/dns/static" {
		t.Errorf("unexpected request %s %s", req.Method, req.Path)
	}
	want := map[string]string{"name": "foo.example.com", "type": "A", "address": "192.0.2.1", "comment": "dexd-mikrotik", "ttl": "300s"}
	if len(req.Body) != len(want) {
		t.Errorf("body = %v, want %v", req.Body, want)
	}
	for k, v := range want {
		if req.Body[k] != v {
			t.Errorf("body[%q] = %q, want %q", k, req.Body[k], v)
		}
	}
}

func TestCreateRecord_TXT_NoTTLAndExactText(t *testing.T) {
	c, sim := newTestClient(t, func(o *Options) { o.DefaultTTL = 300 })
	value := "heritage=dexd-mikrotik,dexd-mikrotik/owner=o,dexd-mikrotik/resource=docker/c"
	if _, err := c.CreateRecord(context.Background(), dns.Record{Name: "a-foo.example.com", Type: "TXT", Value: value}); err != nil {
		t.Fatal(err)
	}
	body := lastRequest(t, sim).Body
	if body["text"] != value {
		t.Errorf("text = %q, want %q", body["text"], value)
	}
	if _, ok := body["ttl"]; ok {
		t.Error("TXT create must not send ttl")
	}
}

func TestCreateRecord_AutoTTLOmitsTTL(t *testing.T) {
	c, sim := newTestClient(t, nil)
	if _, err := c.CreateRecord(context.Background(), dns.Record{Name: "foo.example.com", Type: "A", Value: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := lastRequest(t, sim).Body["ttl"]; ok {
		t.Error("expected no ttl key")
	}
}

func TestUpdateRecord_PatchesOnlyValueAndComment(t *testing.T) {
	c, sim := newTestClient(t, func(o *Options) { o.DefaultTTL = 300 })
	sim.Seed(
		map[string]string{"name": "foo.example.com", "address": "192.0.2.1"},
		map[string]string{"name": "bar.example.com", "type": "CNAME", "cname": "old.example.com"},
	)
	_, err := c.UpdateRecord(context.Background(), dns.Record{ID: "*2", Name: "bar.example.com", Type: "CNAME", Value: "foo.example.com", TTL: 60})
	if err != nil {
		t.Fatal(err)
	}
	req := lastRequest(t, sim)
	if req.Method != http.MethodPatch || req.Path != "/rest/ip/dns/static/*2" {
		t.Errorf("unexpected request %s %s", req.Method, req.Path)
	}
	if len(req.Body) != 2 || req.Body["cname"] != "foo.example.com" || req.Body["comment"] != Comment {
		t.Errorf("body = %v", req.Body)
	}
}

func TestDeleteRecord_RawID(t *testing.T) {
	c, sim := newTestClient(t, nil)
	sim.Seed(map[string]string{"name": "a"}, map[string]string{"name": "b"}, map[string]string{"name": "c"})
	if err := c.DeleteRecord(context.Background(), "*3", "c", "A"); err != nil {
		t.Fatal(err)
	}
	req := lastRequest(t, sim)
	if req.Method != http.MethodDelete || req.Path != "/rest/ip/dns/static/*3" {
		t.Errorf("unexpected request %s %s", req.Method, req.Path)
	}
	if n := len(sim.Entries()); n != 2 {
		t.Errorf("entries = %d, want 2", n)
	}
}

func TestDeleteRecord_NotFoundIsSuccess(t *testing.T) {
	c, sim := newTestClient(t, nil)
	if err := c.DeleteRecord(context.Background(), "*99", "x", "A"); err != nil {
		t.Fatalf("err = %v, want nil for an already-gone entry", err)
	}
	if req := lastRequest(t, sim); req.Method != http.MethodDelete || req.Path != "/rest/ip/dns/static/*99" {
		t.Errorf("unexpected request %s %s", req.Method, req.Path)
	}
}

func TestDeleteRecord_ServerErrorIsAPIError(t *testing.T) {
	c, sim := newTestClient(t, nil)
	sim.Seed(map[string]string{"name": "x.example.com", "address": "192.0.2.1"})
	sim.Fail = map[string]int{http.MethodDelete: http.StatusInternalServerError}
	err := c.DeleteRecord(context.Background(), "*1", "x.example.com", "A")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err = %v, want 500 *APIError", err)
	}
	if IsNotFound(err) {
		t.Error("IsNotFound(500) = true")
	}
}

func TestIsNotFound(t *testing.T) {
	notFound := &APIError{StatusCode: http.StatusNotFound}
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("boom"), false},
		{&APIError{StatusCode: http.StatusInternalServerError}, false},
		{notFound, true},
		{fmt.Errorf("wrapped: %w", notFound), true},
	}
	for _, tc := range cases {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestSystemResource_Unauthorized(t *testing.T) {
	c, sim := newTestClient(t, func(o *Options) { o.Password = "wrong-password" })
	_, err := c.SystemResource(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("err = %v, want 401 *APIError", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, sim.URL) || !strings.Contains(msg, "401") {
		t.Errorf("message %q lacks URL or 401", msg)
	}
	if strings.Contains(msg, "wrong-password") {
		t.Errorf("message leaks password: %q", msg)
	}
}

func TestSystemResource_ReturnsVersion(t *testing.T) {
	c, _ := newTestClient(t, nil)
	res, err := c.SystemResource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "7.25.1 (stable)" || res.BoardName != "RB5009UG+S+" {
		t.Errorf("res = %+v", res)
	}
}

func TestClient_Timeout(t *testing.T) {
	c, sim := newTestClient(t, func(o *Options) { o.Timeout = 50 * time.Millisecond })
	sim.Delay = 200 * time.Millisecond
	_, err := c.ListRecords(context.Background())
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("err = %v, want *NetworkError", err)
	}
}

func TestDryRun_NoMutatingRequests(t *testing.T) {
	c, sim := newTestClient(t, func(o *Options) { o.DryRun = true })
	ctx := context.Background()
	r := dns.Record{ID: "*1", Name: "foo.example.com", Type: "A", Value: "192.0.2.1"}
	if _, err := c.CreateRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRecord(ctx, r.ID, r.Name, r.Type); err != nil {
		t.Fatal(err)
	}
	for _, req := range sim.Requests() {
		if req.Method != http.MethodGet {
			t.Errorf("unexpected %s %s in dry-run", req.Method, req.Path)
		}
	}
}

func TestValidateRecord_TXTTooLong(t *testing.T) {
	c := NewClient(Options{})
	var unsupported *UnsupportedRecordError
	if err := c.ValidateRecord(dns.Record{Name: "t.example.com", Type: "TXT", Value: strings.Repeat("x", 255)}); err != nil {
		t.Errorf("255 bytes: %v", err)
	}
	if err := c.ValidateRecord(dns.Record{Name: "t.example.com", Type: "TXT", Value: strings.Repeat("x", 256)}); !errors.As(err, &unsupported) {
		t.Errorf("256 bytes: err = %v, want *UnsupportedRecordError", err)
	}
	if err := c.ValidateRecord(dns.Record{Name: "m.example.com", Type: "MX", Value: "x"}); !errors.As(err, &unsupported) {
		t.Errorf("MX: err = %v, want *UnsupportedRecordError", err)
	}
}

func TestValidateRecord_WildcardRejected(t *testing.T) {
	c := NewClient(Options{})
	for _, r := range []dns.Record{
		{Name: "*.s3.example.com", Type: "A", Value: "192.0.2.1"},
		{Name: "*.example.com", Type: "CNAME", Value: "target.example.com"},
		{Name: "a-*.example.com", Type: "TXT", Value: "heritage=x"},
	} {
		err := c.ValidateRecord(r)
		var unsupported *UnsupportedRecordError
		if !errors.As(err, &unsupported) {
			t.Errorf("%s %s: err = %v, want *UnsupportedRecordError", r.Type, r.Name, err)
			continue
		}
		if !strings.Contains(unsupported.Reason, "wildcard") {
			t.Errorf("%s %s: reason %q does not mention wildcards", r.Type, r.Name, unsupported.Reason)
		}
	}
	if err := c.ValidateRecord(dns.Record{Name: "s3.example.com", Type: "A", Value: "192.0.2.1"}); err != nil {
		t.Errorf("concrete name: %v", err)
	}
}

func TestTLS_VerifyByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"7.25.1","board-name":"x"}`))
	}))
	defer srv.Close()

	strict := NewClient(Options{BaseURL: srv.URL, Username: "u", Password: "p"})
	_, err := strict.SystemResource(context.Background())
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("err = %v, want *NetworkError", err)
	}

	lax := NewClient(Options{BaseURL: srv.URL, Username: "u", Password: "p", SkipTLSVerify: true})
	if _, err := lax.SystemResource(context.Background()); err != nil {
		t.Fatalf("skip verify: %v", err)
	}
}

func TestDeleteRecord_InvalidIDRejected(t *testing.T) {
	c, sim := newTestClient(t, nil)
	for _, id := range []string{"../system", "*1/x", ""} {
		err := c.DeleteRecord(context.Background(), id, "x", "A")
		var dataErr *DataError
		if !errors.As(err, &dataErr) {
			t.Errorf("id %q: err = %v, want *DataError", id, err)
		}
	}
	if n := len(sim.Requests()); n != 0 {
		t.Errorf("made %d requests, want 0", n)
	}
}

func TestCreateRecord_EmptyIDIsDataError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL})
	_, err := c.CreateRecord(context.Background(), dns.Record{Name: "a.example.com", Type: "A", Value: "192.0.2.1"})
	var dataErr *DataError
	if !errors.As(err, &dataErr) {
		t.Fatalf("err = %v, want *DataError", err)
	}
}
