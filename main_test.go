package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKubeconfigPathPrefersFunnelManagerEnv(t *testing.T) {
	t.Setenv("FUNNEL_MANAGER_KUBECONFIG", "/tmp/funnel-manager")
	t.Setenv("KUBECONFIG", "/tmp/standard")

	if got := kubeconfigPath(); got != "/tmp/funnel-manager" {
		t.Fatalf("kubeconfigPath() = %q, want FUNNEL_MANAGER_KUBECONFIG", got)
	}
}

func TestKubeconfigPathFallsBackToStandardEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/standard")

	if got := kubeconfigPath(); got != "/tmp/standard" {
		t.Fatalf("kubeconfigPath() = %q, want KUBECONFIG", got)
	}
}

func TestKubeconfigPathEmptyForInClusterDefault(t *testing.T) {
	if got := kubeconfigPath(); got != "" {
		t.Fatalf("kubeconfigPath() = %q, want empty", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestObserveRecordsStatus(t *testing.T) {
	h := observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/services", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
}
