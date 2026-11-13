package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/binaly-ai/sync/internal/creds"
)

func expiredSource(t *testing.T, tokenURL string) *Source {
	t.Helper()
	s := NewSource("k", "client", tokenURL, creds.Memory{})
	if err := s.Save(Secret{Access: "old", Refresh: "r1", Expiry: time.Now().Add(-time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	return s
}

// The server restarting answers a token refresh with a proxy 502: the request never reached
// it, so the refresh token is unspent and one retry renews the sign-in (2026-10-26).
func TestRefreshRetriesAProxy502(t *testing.T) {
	retryWait = 10 * time.Millisecond
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "r2", "expires_in": 3600})
	}))
	defer srv.Close()
	tok, err := expiredSource(t, srv.URL).Token(context.Background())
	if err != nil || tok != "new" {
		t.Fatalf("got %q, %v; want the renewed token", tok, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d requests, want 2", calls.Load())
	}
}

// A 504 may come after the server rotated the token; resending the old one would sign this
// computer out, so it isn't retried.
func TestRefreshDoesNotRetryA504(t *testing.T) {
	retryWait = 10 * time.Millisecond
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()
	if _, err := expiredSource(t, srv.URL).Token(context.Background()); err == nil {
		t.Fatal("a 504 renewed the sign-in")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d requests, want 1", calls.Load())
	}
}

// A paused lab (subscription lapsed): the server refuses the renewal with
// subscription_inactive and keeps the refresh token, so this is not a sign-out. The saved
// sign-in must survive, and the same token renews once the lab is back (2026-11-13).
func TestRefreshPausedKeepsTheSignIn(t *testing.T) {
	var paused atomic.Bool
	paused.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("refresh_token") != "r1" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		if paused.Load() {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "subscription_inactive", "error_description": "paused, renew at /subscription"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "r2", "expires_in": 3600})
	}))
	defer srv.Close()
	s := expiredSource(t, srv.URL)
	_, err := s.Token(context.Background())
	if !errors.Is(err, ErrPaused) || errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("got %v; want ErrPaused, not a sign-in", err)
	}
	if !strings.Contains(err.Error(), "renew at /subscription") {
		t.Fatalf("the server's explanation was lost: %v", err)
	}
	paused.Store(false)
	tok, err := s.Token(context.Background())
	if err != nil || tok != "new" {
		t.Fatalf("got %q, %v; want the same sign-in to renew once the lab is back", tok, err)
	}
}
