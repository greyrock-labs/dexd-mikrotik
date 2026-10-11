package mikrotik

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

const (
	testUser = "dexd"
	testPass = "secret"
	basePath = "/rest/ip/dns/static"
)

type recordedRequest struct {
	Method      string
	Path        string
	User        string
	Pass        string
	ContentType string
	Body        map[string]string
}

// fakeRouterOS is a minimal RouterOS REST endpoint that records requests and
// replies with a fixed status and body.
type fakeRouterOS struct {
	server   *httptest.Server
	requests []recordedRequest
	status   int
	body     string
}

func newFakeRouterOS(t *testing.T, status int, body string) *fakeRouterOS {
	t.Helper()
	f := &fakeRouterOS{status: status, body: body}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		req := recordedRequest{
			Method:      r.Method,
			Path:        r.URL.Path,
			User:        user,
			Pass:        pass,
			ContentType: r.Header.Get("Content-Type"),
		}
		_ = json.NewDecoder(r.Body).Decode(&req.Body)
		f.requests = append(f.requests, req)
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRouterOS) client(t *testing.T, dryRun bool, ttl int) *Client {
	t.Helper()
	c, err := NewClient(f.server.URL+"/", testUser, testPass, false, "", dryRun, ttl)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func (f *fakeRouterOS) only(t *testing.T) recordedRequest {
	t.Helper()
	if len(f.requests) != 1 {
		t.Fatalf("got %d requests, want 1: %+v", len(f.requests), f.requests)
	}
	return f.requests[0]
}

func TestListRecords_MapsWireFormat(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusOK, `[
		{".id":"*1","name":"foo.example.com","address":"10.0.0.1","ttl":"1d","disabled":"false","dynamic":"false"},
		{".id":"*2","name":"bar.example.com","type":"CNAME","cname":"foo.example.com","ttl":"1d"},
		{".id":"*3","name":"a-foo.example.com","type":"TXT","text":"heritage=dexd,dexd/owner=dexd,dexd/resource=docker/foo"},
		{".id":"*4","regexp":"^.+\\.foo\\.example\\.com$","type":"A","address":"10.0.0.1"},
		{".id":"*5","regexp":".*\\.ads\\.example$","type":"NXDOMAIN"},
		{".id":"*6","name":"lan","type":"FWD","forward-to":"10.0.0.53"},
		{".id":"*7","name":"v6.example.com","address":"fd00::1"}
	]`)

	got, err := f.client(t, false, 0).ListRecords(context.Background())
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}

	want := []DNSRecord{
		{ID: "*1", Key: "foo.example.com", RecordType: "A", Value: "10.0.0.1"},
		{ID: "*2", Key: "bar.example.com", RecordType: "CNAME", Value: "foo.example.com"},
		{ID: "*3", Key: "a-foo.example.com", RecordType: "TXT", Value: "heritage=dexd,dexd/owner=dexd,dexd/resource=docker/foo"},
		{ID: "*4", Key: "*.foo.example.com", RecordType: "A", Value: "10.0.0.1"},
		{ID: "*7", Key: "v6.example.com", RecordType: "AAAA", Value: "fd00::1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRecords =\n%+v\nwant\n%+v", got, want)
	}

	req := f.only(t)
	if req.Method != http.MethodGet || req.Path != basePath {
		t.Fatalf("request = %s %s, want GET %s", req.Method, req.Path, basePath)
	}
	if req.User != testUser || req.Pass != testPass {
		t.Fatalf("basic auth = %q/%q, want %q/%q", req.User, req.Pass, testUser, testPass)
	}
}

func TestCreateRecord_AutoTTLOmitsTTL(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusCreated, `{".id":"*A","name":"foo.example.com","type":"A","address":"10.0.0.1"}`)

	got, err := f.client(t, false, 0).CreateRecord(context.Background(), DNSRecord{Key: "foo.example.com", RecordType: "A", Value: "10.0.0.1"})
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if got.ID != "*A" {
		t.Fatalf("ID = %q, want *A", got.ID)
	}

	req := f.only(t)
	if req.Method != http.MethodPut || req.Path != basePath {
		t.Fatalf("request = %s %s, want PUT %s", req.Method, req.Path, basePath)
	}
	if req.ContentType != "application/json" {
		t.Fatalf("Content-Type = %q", req.ContentType)
	}
	want := map[string]string{"name": "foo.example.com", "type": "A", "address": "10.0.0.1"}
	if !reflect.DeepEqual(req.Body, want) {
		t.Fatalf("body = %v, want %v", req.Body, want)
	}
}

func TestCreateRecord_BodiesByType(t *testing.T) {
	tests := []struct {
		name   string
		record DNSRecord
		want   map[string]string
	}{
		{
			name:   "cname",
			record: DNSRecord{Key: "foo.example.com", RecordType: "CNAME", Value: "target.example.com"},
			want:   map[string]string{"name": "foo.example.com", "type": "CNAME", "cname": "target.example.com", "ttl": "300s"},
		},
		{
			name:   "txt",
			record: DNSRecord{Key: "a-foo.example.com", RecordType: "TXT", Value: "heritage=dexd,dexd/owner=dexd"},
			want:   map[string]string{"name": "a-foo.example.com", "type": "TXT", "text": "heritage=dexd,dexd/owner=dexd", "ttl": "300s"},
		},
		{
			name:   "wildcard a",
			record: DNSRecord{Key: "*.foo.example.com", RecordType: "A", Value: "10.0.0.1"},
			want:   map[string]string{"regexp": `^.+\.foo\.example\.com$`, "type": "A", "address": "10.0.0.1", "ttl": "300s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRouterOS(t, http.StatusOK, `{".id":"*1"}`)
			if _, err := f.client(t, false, 300).CreateRecord(context.Background(), tt.record); err != nil {
				t.Fatalf("CreateRecord: %v", err)
			}
			if got := f.only(t).Body; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("body = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateRecord_PatchesValueByID(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusOK, `{}`)

	_, err := f.client(t, false, 300).UpdateRecord(context.Background(), DNSRecord{ID: "*1F", Key: "foo.example.com", RecordType: "CNAME", Value: "new.example.com"})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}

	req := f.only(t)
	if req.Method != http.MethodPatch || req.Path != basePath+"/*1F" {
		t.Fatalf("request = %s %s, want PATCH %s/*1F", req.Method, req.Path, basePath)
	}
	want := map[string]string{"cname": "new.example.com", "ttl": "300s"}
	if !reflect.DeepEqual(req.Body, want) {
		t.Fatalf("body = %v, want %v", req.Body, want)
	}
}

func TestDeleteRecord_DeletesByID(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusNoContent, "")

	if err := f.client(t, false, 0).DeleteRecord(context.Background(), "*1F", "foo.example.com", "A"); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}

	req := f.only(t)
	if req.Method != http.MethodDelete || req.Path != basePath+"/*1F" {
		t.Fatalf("request = %s %s, want DELETE %s/*1F", req.Method, req.Path, basePath)
	}
}

func TestDryRunMakesNoMutationCalls(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusOK, `{}`)
	c := f.client(t, true, 0)
	ctx := context.Background()

	if _, err := c.CreateRecord(ctx, DNSRecord{Key: "foo.example.com", RecordType: "A", Value: "10.0.0.1"}); err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if _, err := c.UpdateRecord(ctx, DNSRecord{ID: "*1", Key: "foo.example.com", RecordType: "A", Value: "10.0.0.2"}); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if err := c.DeleteRecord(ctx, "*1", "foo.example.com", "A"); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("dry-run sent requests: %+v", f.requests)
	}
}

func TestAPIErrorIncludesRouterOSDetail(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusBadRequest, `{"error":400,"message":"Bad Request","detail":"failure: entry already exists"}`)

	_, err := f.client(t, false, 0).CreateRecord(context.Background(), DNSRecord{Key: "foo.example.com", RecordType: "A", Value: "10.0.0.1"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "Bad Request: failure: entry already exists" {
		t.Fatalf("APIError = %+v", apiErr)
	}
}

func TestUnauthorizedReturnsAPIError(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusUnauthorized, `{"error":401,"message":"Unauthorized"}`)

	_, err := f.client(t, false, 0).ListRecords(context.Background())

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want 401 *APIError", err)
	}
}

func TestListRecords_InvalidJSONReturnsDataError(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusOK, `not json`)

	_, err := f.client(t, false, 0).ListRecords(context.Background())

	var dataErr *DataError
	if !errors.As(err, &dataErr) {
		t.Fatalf("error = %v, want *DataError", err)
	}
}

func TestNetworkErrorIsTyped(t *testing.T) {
	f := newFakeRouterOS(t, http.StatusOK, `[]`)
	c := f.client(t, false, 0)
	f.server.Close()

	_, err := c.ListRecords(context.Background())

	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("error = %v, want *NetworkError", err)
	}
}

func TestNewClientRejectsBadCACert(t *testing.T) {
	if _, err := NewClient("https://router", testUser, testPass, false, "/nonexistent/ca.pem", false, 0); err == nil {
		t.Fatal("NewClient accepted a missing CA cert file")
	}
}

func TestValidateRecord(t *testing.T) {
	c := &Client{}
	for _, key := range []string{"foo.example.com", "*.foo.example.com"} {
		if err := c.ValidateRecord(DNSRecord{Key: key, RecordType: "CNAME"}); err != nil {
			t.Errorf("ValidateRecord(%q) = %v, want nil", key, err)
		}
	}
	for _, key := range []string{"foo.*.example.com", "*.*.example.com"} {
		var unsupported *UnsupportedRecordError
		if err := c.ValidateRecord(DNSRecord{Key: key, RecordType: "A"}); !errors.As(err, &unsupported) {
			t.Errorf("ValidateRecord(%q) = %v, want *UnsupportedRecordError", key, err)
		}
	}
}

func TestWildcardRegexpRoundTrip(t *testing.T) {
	for _, key := range []string{"*.example.com", "*.s3.home-lab.example.com"} {
		re := wildcardToRegexp(key)
		got, ok := regexpToWildcard(re)
		if !ok || got != key {
			t.Errorf("round trip %q -> %q -> %q, %v", key, re, got, ok)
		}
	}
	for _, re := range []string{"", `.*\.example\.com$`, `^.+\.example.com$`, `^.+\.$`, `^.+\.example\.com`} {
		if got, ok := regexpToWildcard(re); ok {
			t.Errorf("regexpToWildcard(%q) = %q, want not ok", re, got)
		}
	}
}
