// Package mikrotik talks to the RouterOS v7 REST API for /ip/dns/static.
// REST API docs: https://help.mikrotik.com/docs/display/ROS/REST+API
package mikrotik

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	appmetrics "github.com/greyrock-labs/dexd-mikrotik/internal/metrics"
)

// Client talks to the RouterOS static DNS REST API.
type Client struct {
	http       *http.Client
	baseURL    string
	username   string
	password   string
	dryRun     bool
	defaultTTL int
}

// NewClient creates a RouterOS client using HTTP basic auth.
// baseURL is the router's web service URL, e.g. https://192.168.88.1.
// caCertPath optionally points to a PEM file trusted in addition to the system pool.
func NewClient(baseURL, username, password string, insecureSkipVerify bool, caCertPath string, dryRun bool, defaultTTL int) (*Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify} //nolint:gosec
	if caCertPath != "" {
		pem, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA cert contains no valid PEM certificates")
		}
		tlsConfig.RootCAs = pool
	}

	return &Client{
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
			Timeout:   15 * time.Second,
		},
		baseURL:    strings.TrimRight(baseURL, "/") + "/rest/ip/dns/static",
		username:   username,
		password:   password,
		dryRun:     dryRun,
		defaultTTL: defaultTTL,
	}, nil
}

// ValidateRecord rejects records RouterOS static DNS cannot represent.
func (c *Client) ValidateRecord(r DNSRecord) error {
	if strings.Contains(strings.TrimPrefix(r.Key, wildcardPrefix), "*") {
		return &UnsupportedRecordError{
			Key:        r.Key,
			RecordType: r.RecordType,
			Reason:     "only a single leading *. wildcard label is supported",
		}
	}
	return nil
}

// ListRecords returns all A, AAAA, CNAME, and TXT static DNS entries that dexd
// can represent. Other types and hand-written regexp entries are omitted.
func (c *Client) ListRecords(ctx context.Context) ([]DNSRecord, error) {
	var wire []wireRecord
	if err := c.do(ctx, http.MethodGet, c.baseURL, nil, &wire); err != nil {
		return nil, fmt.Errorf("list records: %w", err)
	}
	records := make([]DNSRecord, 0, len(wire))
	for _, w := range wire {
		if r, ok := fromWire(w); ok {
			records = append(records, r)
		}
	}
	slog.Debug("listed mikrotik records", "count", len(records), "total", len(wire))
	return records, nil
}

// CreateRecord creates a new static DNS entry and returns it with its ID.
func (c *Client) CreateRecord(ctx context.Context, r DNSRecord) (DNSRecord, error) {
	body := toWire(r)
	body.TTL = c.ttl()

	if c.dryRun {
		slog.Info("[dry-run] would create record", "key", r.Key, "type", r.RecordType, "value", r.Value)
		return r, nil
	}

	var created wireRecord
	if err := c.do(ctx, http.MethodPut, c.baseURL, body, &created); err != nil {
		return DNSRecord{}, fmt.Errorf("create record %s %s: %w", r.RecordType, r.Key, err)
	}
	r.ID = created.ID
	slog.Info("created record", "key", r.Key, "type", r.RecordType, "value", r.Value, "id", r.ID)
	return r, nil
}

// UpdateRecord changes the value (and TTL, if configured) of an entry by ID.
// The record type cannot change; the controller replaces records for that.
func (c *Client) UpdateRecord(ctx context.Context, r DNSRecord) (DNSRecord, error) {
	var body wireRecord
	setWireValue(&body, r.RecordType, r.Value)
	body.TTL = c.ttl()

	url := c.recordURL(r.ID)
	if c.dryRun {
		slog.Info("[dry-run] would update record", "key", r.Key, "type", r.RecordType, "value", r.Value, "id", r.ID)
		return r, nil
	}

	if err := c.do(ctx, http.MethodPatch, url, body, nil); err != nil {
		return DNSRecord{}, fmt.Errorf("update record %s %s: %w", r.RecordType, r.Key, err)
	}
	slog.Info("updated record", "key", r.Key, "type", r.RecordType, "value", r.Value, "id", r.ID)
	return r, nil
}

// DeleteRecord removes an entry by ID.
func (c *Client) DeleteRecord(ctx context.Context, id, key, recordType string) error {
	url := c.recordURL(id)
	if c.dryRun {
		slog.Info("[dry-run] would delete record", "key", key, "type", recordType, "id", id)
		return nil
	}

	if err := c.do(ctx, http.MethodDelete, url, nil, nil); err != nil {
		return fmt.Errorf("delete record %s %s (id=%s): %w", recordType, key, id, err)
	}
	slog.Info("deleted record", "key", key, "type", recordType, "id", id)
	return nil
}

// recordURL builds the per-entry URL. RouterOS IDs look like "*1A" and are
// sent unescaped, as the external-dns MikroTik webhook does.
func (c *Client) recordURL(id string) string {
	return c.baseURL + "/" + id
}

// ttl returns the RouterOS TTL for new and updated entries, or "" to keep
// the router default (24h) when DEFAULT_TTL=auto.
func (c *Client) ttl() string {
	if c.defaultTTL <= 0 {
		return ""
	}
	return fmt.Sprintf("%ds", c.defaultTTL)
}

func (c *Client) do(ctx context.Context, method, url string, body, out any) error {
	start := time.Now()
	statusCode := 0
	errType := ""
	defer func() {
		appmetrics.ObserveProviderRequest(method, statusCode, time.Since(start), errType)
	}()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			errType = "data"
			return &DataError{Operation: "marshal", DataType: "request body", Err: err}
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		errType = "data"
		return &DataError{Operation: "create request", DataType: "HTTP request", Err: err}
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		errType = "network"
		return &NetworkError{Operation: method, URL: url, Err: err}
	}
	defer resp.Body.Close()
	statusCode = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errType = "api"
		return readAPIError(resp, method, url)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		errType = "data"
		return &DataError{Operation: "read", DataType: "response body", Err: err}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			errType = "data"
			return &DataError{Operation: "unmarshal", DataType: "response body", Err: fmt.Errorf("%w (body: %s)", err, string(respBody))}
		}
	}
	return nil
}

func readAPIError(resp *http.Response, method, url string) error {
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	if err != nil {
		return &DataError{Operation: "read", DataType: "error response body", Err: err}
	}

	apiErr := &APIError{
		Operation:  method,
		URL:        url,
		StatusCode: resp.StatusCode,
		Body:       string(respBody),
	}
	var rosErr errorResponse
	if err := json.Unmarshal(respBody, &rosErr); err == nil {
		switch {
		case rosErr.Detail != "" && rosErr.Message != "":
			apiErr.Message = rosErr.Message + ": " + rosErr.Detail
		case rosErr.Detail != "":
			apiErr.Message = rosErr.Detail
		default:
			apiErr.Message = rosErr.Message
		}
	}
	return apiErr
}
