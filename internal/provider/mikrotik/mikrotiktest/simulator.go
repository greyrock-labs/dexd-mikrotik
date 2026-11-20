// Package mikrotiktest provides an in-process fake of the RouterOS REST API
// for tests. It deliberately uses plain string maps so it does not import the
// package under test.
package mikrotiktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	user = "dexd"
	pass = "secret"

	staticPrefix = "/rest/ip/dns/static"
)

// Request is a recorded HTTP request.
type Request struct {
	Method string
	Path   string // escaped path, e.g. /rest/ip/dns/static/*3
	Body   map[string]string
}

// Sim is a fake RouterOS router.
type Sim struct {
	URL string
	// Delay is applied before every response.
	Delay time.Duration
	// Fail forces a status code per HTTP method (after authentication).
	Fail map[string]int

	srv      *httptest.Server
	mu       sync.Mutex
	entries  []map[string]string
	requests []Request
	next     int
}

// New starts a TLS simulator that accepts dexd/secret and stops with the test.
func New(t testing.TB) *Sim {
	t.Helper()
	s := &Sim{Fail: map[string]int{}, next: 1}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Username returns the accepted username.
func (s *Sim) Username() string { return user }

// Password returns the accepted password.
func (s *Sim) Password() string { return pass }

// Client returns an HTTP client that trusts the simulator's certificate.
func (s *Sim) Client() *http.Client { return s.srv.Client() }

// Seed adds entries, assigning IDs *1, *2, ... in order.
func (s *Sim) Seed(entries ...map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		s.add(e)
	}
}

// Entries returns a copy of the stored entries, including ".id".
func (s *Sim) Entries() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]string, len(s.entries))
	for i, e := range s.entries {
		out[i] = clone(e)
	}
	return out
}

// Requests returns all requests received so far.
func (s *Sim) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Sim) add(e map[string]string) map[string]string {
	c := clone(e)
	c[".id"] = fmt.Sprintf("*%X", s.next)
	s.next++
	s.entries = append(s.entries, c)
	return c
}

func clone(m map[string]string) map[string]string {
	c := make(map[string]string, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (s *Sim) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	path := r.URL.EscapedPath()
	encodedID := strings.Contains(strings.ToUpper(r.RequestURI), "%2A")

	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: path, Body: body})
	delay := s.Delay
	forced := s.Fail[r.Method]
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	u, p, ok := r.BasicAuth()
	if !ok || u != user || p != pass {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "")
		return
	}
	if forced != 0 {
		writeErr(w, forced, http.StatusText(forced), "forced failure")
		return
	}

	switch {
	case path == "/rest/system/resource" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]string{"version": "7.25.1 (stable)", "board-name": "RB5009UG+S+"})
	case path == staticPrefix && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, s.entries)
	case path == staticPrefix && r.Method == http.MethodPut:
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusCreated, s.add(body))
	case strings.HasPrefix(path, staticPrefix+"/") && (r.Method == http.MethodPatch || r.Method == http.MethodDelete):
		s.mu.Lock()
		defer s.mu.Unlock()
		if encodedID {
			// Real RouterOS rejects percent-encoded ids.
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "missing or invalid resource identifier", "error": http.StatusBadRequest, "message": "Bad Request",
			})
			return
		}
		s.mutate(w, r.Method, strings.TrimPrefix(path, staticPrefix+"/"), body)
	default:
		writeErr(w, http.StatusNotFound, "Not Found", "no such command or directory")
	}
}

// mutate handles PATCH and DELETE; the caller holds s.mu.
func (s *Sim) mutate(w http.ResponseWriter, method, id string, body map[string]string) {
	for i, e := range s.entries {
		if e[".id"] != id {
			continue
		}
		if method == http.MethodDelete {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		for k, v := range body {
			e[k] = v
		}
		writeJSON(w, http.StatusOK, e)
		return
	}
	writeErr(w, http.StatusNotFound, "Not Found", "no such item")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, detail string) {
	b := map[string]any{"error": status, "message": msg}
	if detail != "" {
		b["detail"] = detail
	}
	writeJSON(w, status, b)
}
