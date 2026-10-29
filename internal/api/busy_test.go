package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type staticTokens struct{}

func (staticTokens) Token(context.Context) (string, error) { return "t", nil }
func (staticTokens) Invalidate()                           {}

func TestBusyDelayIsRandomAndCapped(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		d := busyDelay("20")
		if d < 10*time.Second || d > 30*time.Second {
			t.Fatalf("busyDelay(20) = %v, want 10-30 s", d)
		}
		seen[d] = true
	}
	if len(seen) < 10 {
		t.Errorf("waits aren't random: %d distinct in 50", len(seen))
	}
	if d := busyDelay("100000"); d > busyWaitMax {
		t.Errorf("a huge Retry-After waits %v, over the %v cap", d, busyWaitMax)
	}
	if d := busyDelay(""); d < 7*time.Second || d > 23*time.Second {
		t.Errorf("no Retry-After waits %v, want around 15 s", d)
	}
}

func TestBusyIsRetriedThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ok":false,"error":"busy","retryAfter":1}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := &Client{Server: srv.URL, Tokens: staticTokens{}}
	start := time.Now()
	if err := c.do(context.Background(), "POST", "/sync/v1/extract", map[string]any{"x": 1}, nil); err != nil {
		t.Fatalf("busy twice then ok: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if el := time.Since(start); el < 900*time.Millisecond || el > 4*time.Second {
		t.Errorf("two waits of about a second took %v", el)
	}
}

func TestBusyGivesUpWithinTheBudget(t *testing.T) {
	oldMax, oldBudget := busyWaitMax, busyBudget
	busyWaitMax, busyBudget = 300*time.Millisecond, time.Second
	defer func() { busyWaitMax, busyBudget = oldMax, oldBudget }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ok":false,"error":"busy"}`))
	}))
	defer srv.Close()
	c := &Client{Server: srv.URL, Tokens: staticTokens{}}
	start := time.Now()
	err := c.do(context.Background(), "POST", "/sync/v1/report", map[string]any{}, nil)
	var ae *Error
	if err == nil || !errors.As(err, &ae) || ae.Code != "busy" {
		t.Fatalf("want a busy error after the budget, got %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("gave up after %v, over the 1 s budget", el)
	}
}

func TestProxy503IsStillRetriedOnce(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("<html>upstream unavailable</html>"))
	}))
	defer srv.Close()
	old := gatewayRetryWait
	gatewayRetryWait = 10 * time.Millisecond
	defer func() { gatewayRetryWait = old }()
	c := &Client{Server: srv.URL, Tokens: staticTokens{}}
	if err := c.do(context.Background(), "POST", "/sync/v1/report", map[string]any{}, nil); err == nil {
		t.Fatal("a proxy 503 that never clears should fail")
	}
	if calls != 2 {
		t.Errorf("a proxy 503 was sent %d times, want 2", calls)
	}
}
