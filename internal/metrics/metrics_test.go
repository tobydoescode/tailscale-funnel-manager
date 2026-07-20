package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCounterVec_RendersPrometheusTextFormat(t *testing.T) {
	c := NewCounterVec("test_requests_total", "Test requests.", "route", "code")
	c.Inc("/api/services", "200")
	c.Inc("/api/services", "200")
	c.Inc("/api/services", "500")

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	for _, want := range []string{
		"# HELP test_requests_total Test requests.",
		"# TYPE test_requests_total counter",
		`test_requests_total{route="/api/services",code="200"} 2`,
		`test_requests_total{route="/api/services",code="500"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestCounterVec_NoLabels(t *testing.T) {
	c := NewCounterVec("test_plain_total", "Plain counter.")
	c.Inc()

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "test_plain_total 1") {
		t.Errorf("plain counter not rendered:\n%s", rec.Body.String())
	}
}

func TestCounterVec_IgnoresMismatchedLabelCount(t *testing.T) {
	c := NewCounterVec("test_mismatch_total", "Mismatch.", "a")
	c.Inc()
	c.Inc("x", "y")

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(rec.Body.String(), "test_mismatch_total{") {
		t.Errorf("mismatched Inc should be dropped:\n%s", rec.Body.String())
	}
}

func TestEscapeLabelValue(t *testing.T) {
	got := escapeLabelValue("a\"b\\c\nd")
	want := `a\"b\\c\nd`
	if got != want {
		t.Errorf("escape = %q, want %q", got, want)
	}
}
