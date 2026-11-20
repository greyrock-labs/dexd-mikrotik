package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadPolicy(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want Policy
	}{
		{name: "default", want: PolicySync},
		{name: "sync", env: "sync", want: PolicySync},
		{name: "upsert-only", env: "upsert-only", want: PolicyUpsertOnly},
		{name: "create-only", env: "create-only", want: PolicyCreateOnly},
		{name: "trim and lowercase", env: " UPSERT-ONLY ", want: PolicyUpsertOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			if tt.env != "" {
				t.Setenv("POLICY", tt.env)
			}

			got, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.Policy != tt.want {
				t.Fatalf("Policy = %q, want %q", got.Policy, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidPolicy(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("POLICY", "delete-only")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid POLICY") {
		t.Fatalf("Load() error = %q, want invalid policy error", err)
	}
}

func TestLoadDefaultsPolicyAndInterval(t *testing.T) {
	setRequiredEnv(t)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Policy != PolicySync {
		t.Fatalf("Policy = %q, want %q", got.Policy, PolicySync)
	}
	if got.ReconcileInterval != 5*time.Minute {
		t.Fatalf("ReconcileInterval = %s, want 5m", got.ReconcileInterval)
	}
	if got.DefaultTTL != 0 || got.DefaultTTL.String() != "auto" {
		t.Fatalf("DefaultTTL = %q (%d), want auto", got.DefaultTTL.String(), got.DefaultTTL)
	}
	if got.MetricsAddr != ":8080" {
		t.Fatalf("MetricsAddr = %q, want :8080", got.MetricsAddr)
	}
	if got.OwnerID != "dexd-mikrotik" {
		t.Fatalf("OwnerID = %q, want dexd-mikrotik", got.OwnerID)
	}
}

func TestLoadDefaultTTL(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want DefaultTTL
	}{
		{name: "auto", env: "auto", want: 0},
		{name: "empty is auto", env: "", want: 0},
		{name: "trim and lowercase auto", env: " AUTO ", want: 0},
		{name: "numeric", env: "600", want: 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("DEFAULT_TTL", tt.env)

			got, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.DefaultTTL != tt.want {
				t.Fatalf("DefaultTTL = %q (%d), want %d", got.DefaultTTL.String(), got.DefaultTTL, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidDefaultTTL(t *testing.T) {
	tests := []string{"0", "-1", "soon"}
	for _, ttl := range tests {
		t.Run(ttl, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("DEFAULT_TTL", ttl)

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "invalid DEFAULT_TTL") {
				t.Fatalf("Load() error = %q, want invalid DEFAULT_TTL error", err)
			}
		})
	}
}

func TestLoadAllowsEmptyMetricsAddr(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("METRICS_ADDR", "")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.MetricsAddr != "" {
		t.Fatalf("MetricsAddr = %q, want empty", got.MetricsAddr)
	}
}

func TestLoadRejectsNonPositiveReconcileInterval(t *testing.T) {
	tests := []string{"0s", "-1s"}
	for _, interval := range tests {
		t.Run(interval, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("RECONCILE_INTERVAL", interval)

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "RECONCILE_INTERVAL must be positive") {
				t.Fatalf("Load() error = %q, want positive interval error", err)
			}
		})
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MIKROTIK_BASEURL", "https://192.0.2.1")
	t.Setenv("MIKROTIK_USERNAME", "dexd")
	t.Setenv("MIKROTIK_PASSWORD", "secret")
	t.Setenv("DEFAULT_TARGET", "10.0.0.1")
}

func TestLoad_MikrotikRequired(t *testing.T) {
	for _, name := range []string{"MIKROTIK_BASEURL", "MIKROTIK_USERNAME", "MIKROTIK_PASSWORD"} {
		t.Run(name, func(t *testing.T) {
			setRequiredEnv(t)
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() error = %q, want it to name %s", err, name)
			}
		})
	}
}

func TestLoad_MikrotikDefaults(t *testing.T) {
	setRequiredEnv(t)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.MikrotikSkipTLSVerify {
		t.Fatal("MikrotikSkipTLSVerify = true, want false")
	}
	if got.OwnerID != "dexd-mikrotik" {
		t.Fatalf("OwnerID = %q, want dexd-mikrotik", got.OwnerID)
	}
	if got.MikrotikBaseURL != "https://192.0.2.1" || got.MikrotikUsername != "dexd" || got.MikrotikPassword != "secret" {
		t.Fatalf("unexpected Mikrotik settings: %+v", got)
	}
}

func TestLoad_BaseURLMustBeHTTPS_or_HTTP(t *testing.T) {
	for _, bad := range []string{"192.0.2.1", "ftp://192.0.2.1", "https://"} {
		t.Run(bad, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("MIKROTIK_BASEURL", bad)

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "MIKROTIK_BASEURL") {
				t.Fatalf("Load() error = %q, want it to name MIKROTIK_BASEURL", err)
			}
		})
	}
	setRequiredEnv(t)
	t.Setenv("MIKROTIK_BASEURL", "http://192.0.2.1:8080")
	if _, err := Load(); err != nil {
		t.Fatalf("http base URL rejected: %v", err)
	}
}

func TestCleartextBaseURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://192.0.2.1", false},
		{"http://192.0.2.1", true},
		{"HTTP://192.0.2.1", true},
	} {
		t.Run(tc.url, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("MIKROTIK_BASEURL", tc.url)

			got, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.CleartextBaseURL() != tc.want {
				t.Fatalf("CleartextBaseURL() = %v, want %v", got.CleartextBaseURL(), tc.want)
			}
		})
	}
}
