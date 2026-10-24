package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true},
		{"v0.1.10", "v0.1.9", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.2.0", "v0.2.0-rc.1", true},
		{"v0.2.0-rc.1", "v0.2.0", false},
		{"v0.2.0-rc.2", "v0.2.0-rc.1", true},
		{"v9.9.9", "dev", false}, // a development build never updates itself
		{"garbage", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPublicKeyDecodes(t *testing.T) {
	k, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(k) != ed25519.PublicKeySize {
		t.Fatalf("PublicKey isn't a base64 ed25519 key: %v", err)
	}
}

// releaseServer serves one signed release, the way GitHub's /latest/download/ links do.
func releaseServer(t *testing.T, priv ed25519.PrivateKey, version string, exe []byte, tamper func(m *Manifest)) *httptest.Server {
	sum := sha256.Sum256(exe)
	m := Manifest{Version: version, Files: map[string]string{"binaly-sync.exe": hex.EncodeToString(sum[:])}}
	manifest, _ := json.Marshal(m)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	if tamper != nil {
		tamper(&m)
		manifest, _ = json.Marshal(m) // signature no longer matches
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/download/release.json", func(w http.ResponseWriter, r *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/latest/download/release.json.sig", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sig)) })
	mux.HandleFunc("/download/"+version+"/binaly-sync.exe", func(w http.ResponseWriter, r *http.Request) { w.Write(exe) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestAndDownload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	exe := []byte("MZ pretend executable v0.2.0")
	srv := releaseServer(t, priv, "v0.2.0", exe, nil)
	c := &Checker{Base: srv.URL, PublicKey: pub, HTTP: srv.Client()}
	ctx := context.Background()

	r, err := c.Latest(ctx, "v0.1.0", "amd64")
	if err != nil || r == nil {
		t.Fatalf("Latest: %v, %v", r, err)
	}
	dst := filepath.Join(t.TempDir(), "new.exe")
	if err := c.Download(ctx, r, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != string(exe) {
		t.Fatal("downloaded bytes differ")
	}

	// Already current: nothing to do.
	if r, err := c.Latest(ctx, "v0.2.0", "amd64"); err != nil || r != nil {
		t.Fatalf("current version offered an update: %v, %v", r, err)
	}
	// No build for this architecture.
	if _, err := c.Latest(ctx, "v0.1.0", "arm64"); err == nil {
		t.Fatal("a release without an arm64 build was offered to arm64")
	}
}

func TestRefusesBadSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ctx := context.Background()

	// Manifest changed after signing (e.g. a swapped hash).
	srv := releaseServer(t, priv, "v0.2.0", []byte("exe"), func(m *Manifest) { m.Files["binaly-sync.exe"] = hex.EncodeToString(make([]byte, 32)) })
	c := &Checker{Base: srv.URL, PublicKey: pub, HTTP: srv.Client()}
	if _, err := c.Latest(ctx, "v0.1.0", "amd64"); err == nil {
		t.Fatal("a tampered manifest was accepted")
	}

	// Signed by a different key.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	srv = releaseServer(t, other, "v0.2.0", []byte("exe"), nil)
	c = &Checker{Base: srv.URL, PublicKey: pub, HTTP: srv.Client()}
	if _, err := c.Latest(ctx, "v0.1.0", "amd64"); err == nil {
		t.Fatal("a manifest signed by another key was accepted")
	}
}

func TestRefusesWrongBytes(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := releaseServer(t, priv, "v0.2.0", []byte("the real one"), nil)
	c := &Checker{Base: srv.URL, PublicKey: pub, HTTP: srv.Client()}
	ctx := context.Background()
	r, err := c.Latest(ctx, "v0.1.0", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	r.SHA256 = hex.EncodeToString(make([]byte, 32)) // as if the hosted file were swapped
	dst := filepath.Join(t.TempDir(), "new.exe")
	if err := c.Download(ctx, r, dst); err == nil {
		t.Fatal("a file that doesn't match the manifest was accepted")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a rejected download was left in place")
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Fatal("a rejected download's partial file was left behind")
	}
}
