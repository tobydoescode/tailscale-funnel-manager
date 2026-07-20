package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestBearer_RejectsMissingHeader(t *testing.T) {
	h := Bearer("secret")(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("missing WWW-Authenticate header")
	}
}

func TestBearer_RejectsWrongToken(t *testing.T) {
	h := Bearer("secret")(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer nope")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestBearer_AcceptsCorrectToken(t *testing.T) {
	h := Bearer("secret")(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestBearer_RejectsBasicScheme(t *testing.T) {
	h := Bearer("secret")(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestBearer_RateLimitsRepeatedFailures(t *testing.T) {
	h := Bearer("secret")(okHandler())

	send := func(remoteAddr, token string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < maxFailures; i++ {
		if code := send("10.0.0.1:1234", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, code)
		}
	}
	if code := send("10.0.0.1:1234", "wrong"); code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after %d failures", code, maxFailures)
	}
	// The correct token is also blocked while the IP is in cooldown.
	if code := send("10.0.0.1:1234", "secret"); code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 for blocked IP even with correct token", code)
	}
	// A different IP is unaffected.
	if code := send("10.0.0.2:1234", "secret"); code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for unaffected IP", code)
	}
}

func TestBearer_SuccessResetsFailureCount(t *testing.T) {
	h := Bearer("secret")(okHandler())

	send := func(token string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < maxFailures-1; i++ {
		send("wrong")
	}
	if code := send("secret"); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	// The counter reset: another run of failures is allowed before 429.
	for i := 0; i < maxFailures; i++ {
		if code := send("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d after reset: status = %d, want 401", i, code)
		}
	}
}

func TestBearer_MissingHeaderDoesNotCountTowardLimit(t *testing.T) {
	h := Bearer("secret")(okHandler())

	for i := 0; i < maxFailures*2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestBearer_EmptyTokenPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on empty token")
		}
	}()
	Bearer("")
}
