// Package mikrotik is a RouterOS 7 REST client for static DNS records.
package mikrotik

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/greyrock-labs/dexd-mikrotik/internal/dns"
	appmetrics "github.com/greyrock-labs/dexd-mikrotik/internal/metrics"
)

// Comment marks records owned by this controller.
const Comment = "dexd-mikrotik"

const (
	staticPath   = "/rest/ip/dns/static"
	resourcePath = "/rest/system/resource"
	maxTXTLen    = 255
)

// idPattern matches RouterOS item ids such as "*1A". RouterOS rejects
// percent-encoded ids in paths, so ids are validated and sent raw.
var idPattern = regexp.MustCompile(`^\*[0-9A-Fa-f]+$`)

func itemPath(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", &DataError{Operation: "validate", DataType: "record id", Err: fmt.Errorf("invalid RouterOS id %q", id)}
	}
	return staticPath + "/" + id, nil
}

// Options configures a Client.
type Options struct {
	BaseURL, Username, Password string
	SkipTLSVerify, DryRun       bool
	DefaultTTL                  int           // seconds, 0 = omit
	Timeout                     time.Duration // 0 -> 10s
}

// SystemResource is the subset of /system/resource that is reported.
type SystemResource struct{ Version, BoardName string }

// Client talks to the RouterOS REST API.
type Client struct {
	http *http.Client
	o    Options
}

// NewClient creates a RouterOS client using basic auth.
func NewClient(o Options) *Client {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	// Clone the default transport to keep proxy-from-environment and its
	// dial and TLS handshake timeouts.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: o.SkipTLSVerify} //nolint:gosec
	return &Client{
		http: &http.Client{Timeout: o.Timeout, Transport: transport},
		o:    o,
	}
}

// ValidateRecord rejects records RouterOS static DNS cannot represent.
func (c *Client) ValidateRecord(r dns.Record) error {
	if strings.Contains(r.Name, "*") {
		return &UnsupportedRecordError{
			Name:       r.Name,
			RecordType: r.Type,
			Reason:     "wildcard hostnames are not supported; use a concrete hostname",
		}
	}
	switch r.Type {
	case "A", "CNAME":
	case "TXT":
		if len(r.Value) > maxTXTLen {
			return &UnsupportedRecordError{
				Name:       r.Name,
				RecordType: r.Type,
				Reason:     fmt.Sprintf("TXT value is %d bytes, limit is %d", len(r.Value), maxTXTLen),
			}
		}
	default:
		return &UnsupportedRecordError{Name: r.Name, RecordType: r.Type, Reason: "only A, CNAME and TXT are supported"}
	}
	return nil
}

// SystemResource returns the router version and board name.
func (c *Client) SystemResource(ctx context.Context) (SystemResource, error) {
	var res rosResource
	if err := c.do(ctx, http.MethodGet, resourcePath, nil, &res); err != nil {
		return SystemResource{}, err
	}
	return SystemResource(res), nil
}

// ListRecords returns the A, CNAME and TXT static entries with a name.
func (c *Client) ListRecords(ctx context.Context) ([]dns.Record, error) {
	var raw []rosRecord
	if err := c.do(ctx, http.MethodGet, staticPath, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]dns.Record, 0, len(raw))
	for _, r := range raw {
		if r.Name == "" || r.Regexp != "" {
			continue // regexp entries
		}
		t := r.Type
		if t == "" {
			t = "A"
		}
		rec := dns.Record{ID: r.ID, Name: strings.ToLower(r.Name), Type: t}
		switch t {
		case "A":
			rec.Value = r.Address
		case "CNAME":
			rec.Value = r.CNAME
		case "TXT":
			rec.Value = r.Text
		default:
			continue
		}
		out = append(out, rec)
	}
	slog.Debug("listed routeros records", "count", len(out))
	return out, nil
}

// CreateRecord creates a static entry tagged with Comment.
func (c *Client) CreateRecord(ctx context.Context, r dns.Record) (dns.Record, error) {
	if c.o.DryRun {
		slog.Info("[dry-run] would create record", "name", r.Name, "type", r.Type, "value", r.Value)
		return r, nil
	}
	body := rosRecord{Name: r.Name, Type: r.Type, Comment: Comment}
	setValue(&body, r)
	if r.Type != "TXT" {
		ttl := r.TTL
		if ttl == 0 {
			ttl = c.o.DefaultTTL
		}
		if ttl > 0 {
			body.TTL = strconv.Itoa(ttl) + "s"
		}
	}
	var created rosRecord
	if err := c.do(ctx, http.MethodPut, staticPath, body, &created); err != nil {
		return dns.Record{}, err
	}
	if created.ID == "" {
		return dns.Record{}, &DataError{Operation: "create", DataType: "response body", Err: errors.New("response has no .id")}
	}
	r.ID = created.ID
	slog.Info("created record", "name", r.Name, "type", r.Type, "value", r.Value, "id", r.ID)
	return r, nil
}

// UpdateRecord changes the value (and comment) of an existing entry.
func (c *Client) UpdateRecord(ctx context.Context, r dns.Record) (dns.Record, error) {
	if c.o.DryRun {
		slog.Info("[dry-run] would update record", "name", r.Name, "type", r.Type, "value", r.Value, "id", r.ID)
		return r, nil
	}
	body := rosRecord{Comment: Comment}
	setValue(&body, r)
	path, err := itemPath(r.ID)
	if err != nil {
		return dns.Record{}, err
	}
	if err := c.do(ctx, http.MethodPatch, path, body, nil); err != nil {
		return dns.Record{}, err
	}
	slog.Info("updated record", "name", r.Name, "type", r.Type, "value", r.Value, "id", r.ID)
	return r, nil
}

// DeleteRecord removes an entry by ID. An entry that is already gone (HTTP
// 404) counts as deleted.
func (c *Client) DeleteRecord(ctx context.Context, id, name, recordType string) error {
	if c.o.DryRun {
		slog.Info("[dry-run] would delete record", "name", name, "type", recordType, "id", id)
		return nil
	}
	path, err := itemPath(id)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		if IsNotFound(err) {
			slog.Info("record already deleted", "name", name, "type", recordType, "id", id)
			return nil
		}
		return err
	}
	slog.Info("deleted record", "name", name, "type", recordType, "id", id)
	return nil
}

func setValue(b *rosRecord, r dns.Record) {
	switch r.Type {
	case "CNAME":
		b.CNAME = r.Value
	case "TXT":
		b.Text = r.Value
	default:
		b.Address = r.Value
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	start := time.Now()
	statusCode := 0
	errType := ""
	defer func() {
		appmetrics.ObserveProviderRequest(method, statusCode, time.Since(start), errType)
	}()

	fullURL := c.o.BaseURL + path

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			errType = "data"
			return &DataError{Operation: "marshal", DataType: "request body", Err: err}
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		errType = "data"
		return &DataError{Operation: "create request", DataType: "HTTP request", Err: err}
	}
	req.SetBasicAuth(c.o.Username, c.o.Password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		errType = "network"
		// url.Error repeats the URL; unwrap to keep the message tidy.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &NetworkError{Operation: method, URL: fullURL, Err: err}
	}
	defer resp.Body.Close()
	statusCode = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errType = "api"
		return readAPIError(resp, method, fullURL)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		errType = "data"
		return &DataError{Operation: "read", DataType: "response body", Err: err}
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			errType = "data"
			return &DataError{Operation: "unmarshal", DataType: "response body", Err: err}
		}
	}
	return nil
}

func readAPIError(resp *http.Response, method, fullURL string) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	if err != nil {
		return &DataError{Operation: "read", DataType: "error response body", Err: err}
	}
	apiErr := &APIError{Operation: method, URL: fullURL, StatusCode: resp.StatusCode, Body: string(b)}
	var er errorResponse
	if json.Unmarshal(b, &er) == nil {
		apiErr.Message = er.Message
		apiErr.Detail = er.Detail
	}
	return apiErr
}
