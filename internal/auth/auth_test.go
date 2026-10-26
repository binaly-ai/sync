package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
