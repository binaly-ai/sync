// Package auth signs Binaly Sync in the same way the Binaly connector signs in an AI client:
// OAuth 2.1 with PKCE, in the user's own browser. Sync listens on 127.0.0.1 for the one
// redirect (RFC 8252), so the password is only ever typed into Binaly's own page.
//
// The result is a short-lived access token and a refresh token. The server rotates the
// refresh token on every use, so the new one is saved to the credential store BEFORE the
// access token is used - losing it would sign the machine out.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/binaly-ai/sync/internal/creds"
)

// ErrNeedsSignIn means the saved sign-in is missing, expired or revoked.
var ErrNeedsSignIn = errors.New("needs sign-in")

// Meta is the server's OAuth discovery document (the parts Sync uses).
type Meta struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// Secret is what the credential store holds for one account.
type Secret struct {
	Refresh string `json:"refresh,omitempty"`
	Access  string `json:"access,omitempty"`
	Expiry  int64  `json:"exp,omitempty"`
	// Static is a machine token pasted with `login --token`; used as is, never refreshed.
	Static string `json:"static,omitempty"`
}

var client = &http.Client{Timeout: 30 * time.Second}

// Discover fetches the server's OAuth endpoints.
func Discover(ctx context.Context, server string) (Meta, error) {
	var m Meta
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(server, "/")+"/.well-known/oauth-authorization-server", nil)
	res, err := client.Do(req)
	if err != nil {
		return m, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return m, fmt.Errorf("discovery: HTTP %d", res.StatusCode)
	}
	return m, json.NewDecoder(res.Body).Decode(&m)
}

// redirectPath is the loopback path registered for Sync. Any port is accepted (RFC 8252 7.3).
const redirectPath = "/callback"

// Register registers this install as an OAuth client and returns its client id. There is no
// client secret: a desktop program cannot keep one, and PKCE is what protects the exchange.
func Register(ctx context.Context, m Meta, clientName string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":   clientName,
		"redirect_uris": []string{"http://127.0.0.1" + redirectPath},
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", m.RegistrationEndpoint, strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.ClientID == "" {
		return "", fmt.Errorf("client registration failed (HTTP %d)", res.StatusCode)
	}
	return out.ClientID, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Login runs the browser sign-in and returns the tokens. openBrowser is given the URL to open.
func Login(ctx context.Context, m Meta, clientID string, openBrowser func(string) error) (Secret, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Secret{}, err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, redirectPath)
	verifier := randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomString(16)

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	authURL := m.AuthorizationEndpoint + "?" + q.Encode()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != redirectPath {
			http.NotFound(w, r)
			return
		}
		qs := r.URL.Query()
		var res result
		switch {
		case qs.Get("state") != state:
			res.err = errors.New("sign-in answer didn't match this request; try again")
		case qs.Get("error") != "":
			res.err = fmt.Errorf("sign-in refused: %s", qs.Get("error"))
		case qs.Get("code") == "":
			res.err = errors.New("sign-in returned no code")
		default:
			res.code = qs.Get("code")
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		msg := "You're signed in to Binaly Sync. You can close this tab."
		if res.err != nil {
			msg = "Sign-in didn't complete: " + html.EscapeString(res.err.Error())
		}
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Binaly Sync</title><body style="font-family:system-ui;padding:3rem;background:#F6F1E7;color:#1B2440"><h2>Binaly Sync</h2><p>%s</p></body>`, msg)
		once.Do(func() { done <- res })
	})}
	go srv.Serve(ln)
	defer srv.Close()

	if err := openBrowser(authURL); err != nil {
		return Secret{}, fmt.Errorf("couldn't open a browser (%v); open this address yourself: %s", err, authURL)
	}
	var got result
	select {
	case got = <-done:
	case <-ctx.Done():
		return Secret{}, ctx.Err()
	case <-time.After(10 * time.Minute):
		return Secret{}, errors.New("sign-in timed out")
	}
	if got.err != nil {
		return Secret{}, got.err
	}
	return exchange(ctx, m.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {got.code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	})
}

// retryWait is how long a token request waits before its one retry after a 502 or 503.
var retryWait = 2 * time.Second

func exchange(ctx context.Context, endpoint string, form url.Values) (Secret, error) {
	var res *http.Response
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
		req.Header.Set("content-type", "application/x-www-form-urlencoded")
		var err error
		if res, err = client.Do(req); err != nil {
			return Secret{}, err
		}
		// A proxy's 502 or 503 means the request never reached the server (it was restarting,
		// 2026-10-26), so the refresh token wasn't spent and one retry is safe. Not a 504: the
		// server may have rotated the token, and resending the old one would sign this computer out.
		if (res.StatusCode != http.StatusBadGateway && res.StatusCode != http.StatusServiceUnavailable) || attempt > 0 {
			break
		}
		res.Body.Close()
		select {
		case <-ctx.Done():
			return Secret{}, ctx.Err()
		case <-time.After(retryWait):
		}
	}
	defer res.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Error == "invalid_grant" {
		return Secret{}, ErrNeedsSignIn
	}
	if res.StatusCode != 200 || out.AccessToken == "" {
		return Secret{}, fmt.Errorf("token request failed (HTTP %d %s)", res.StatusCode, out.Error)
	}
	return Secret{
		Access:  out.AccessToken,
		Refresh: out.RefreshToken,
		Expiry:  time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).Unix(),
	}, nil
}

// Source hands out a valid access token for one account, refreshing and saving as needed.
type Source struct {
	mu       sync.Mutex
	key      string
	clientID string
	tokenURL string
	store    creds.Store
}

// NewSource builds a token source. tokenURL may be empty for a static token.
func NewSource(key, clientID, tokenURL string, store creds.Store) *Source {
	return &Source{key: key, clientID: clientID, tokenURL: tokenURL, store: store}
}

// Save stores a fresh secret for this account.
func (s *Source) Save(sec Secret) error {
	raw, _ := json.Marshal(sec)
	return s.store.Set(s.key, string(raw))
}

// Token returns an access token valid for at least a minute.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.store.Get(s.key)
	if err != nil {
		if errors.Is(err, creds.ErrNotFound) {
			return "", ErrNeedsSignIn
		}
		return "", err
	}
	var sec Secret
	if err := json.Unmarshal([]byte(raw), &sec); err != nil {
		return "", ErrNeedsSignIn
	}
	if sec.Static != "" {
		return sec.Static, nil
	}
	if sec.Access != "" && time.Now().Add(time.Minute).Unix() < sec.Expiry {
		return sec.Access, nil
	}
	if sec.Refresh == "" || s.tokenURL == "" {
		return "", ErrNeedsSignIn
	}
	next, err := exchange(ctx, s.tokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {sec.Refresh},
		"client_id":     {s.clientID},
	})
	if err != nil {
		return "", err
	}
	if next.Refresh == "" {
		next.Refresh = sec.Refresh
	}
	// Save first: the old refresh token is already spent on the server.
	if err := s.Save(next); err != nil {
		return "", err
	}
	return next.Access, nil
}

// Invalidate drops a cached access token after the server refused it, forcing a refresh.
func (s *Source) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.store.Get(s.key)
	if err != nil {
		return
	}
	var sec Secret
	if json.Unmarshal([]byte(raw), &sec) != nil || sec.Static != "" {
		return
	}
	sec.Access, sec.Expiry = "", 0
	b, _ := json.Marshal(sec)
	_ = s.store.Set(s.key, string(b))
}
