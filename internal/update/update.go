// Package update keeps Binaly Sync current from this repository's GitHub releases.
//
// Each release carries release.json, which names the version and the SHA-256 of each
// executable, and release.json.sig, an ed25519 signature over those exact bytes made by the
// release workflow. An update is installed only when the signature verifies against the public
// key built into this program, the version is newer than the running one, and the downloaded
// file matches the manifest's hash. Where the files are hosted doesn't matter to its safety.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultBase is where releases are published.
const DefaultBase = "https://github.com/binaly-ai/sync/releases"

// PublicKey verifies release manifests. The private half is a GitHub Actions secret of this
// repository and never leaves it.
const PublicKey = "6JaDqTR2auh9mn9Q/+KRg42f3byITjucRIr275xmDMk="

// maxExe bounds a download, so a broken or hostile server can't fill the disk.
const maxExe = 200 << 20

// Manifest is release.json.
type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"` // file name -> sha256 (hex)
}

// Release is an available update for this computer.
type Release struct {
	Version string
	URL     string
	SHA256  string
}

// Checker finds and downloads releases.
type Checker struct {
	Base      string            // DefaultBase unless testing
	PublicKey ed25519.PublicKey // from PublicKey unless testing
	HTTP      *http.Client
}

// New returns a Checker for the real releases.
func New() *Checker {
	key, _ := base64.StdEncoding.DecodeString(PublicKey)
	return &Checker{Base: DefaultBase, PublicKey: key, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// AssetName is the executable for a processor architecture.
func AssetName(goarch string) string {
	if goarch == "arm64" {
		return "binaly-sync-arm64.exe"
	}
	return "binaly-sync.exe"
}

func (c *Checker) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

// Verify checks a manifest's signature and parses it.
func Verify(key ed25519.PublicKey, manifest, sig []byte) (*Manifest, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, manifest, raw) {
		return nil, errors.New("the release's signature doesn't verify; not installing it")
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, err
	}
	if _, ok := parse(m.Version); !ok {
		return nil, fmt.Errorf("the release has an unreadable version %q", m.Version)
	}
	return &m, nil
}

// Latest returns the newest release if it's newer than current, or nil.
func (c *Checker) Latest(ctx context.Context, current, goarch string) (*Release, error) {
	manifest, err := c.get(ctx, c.Base+"/latest/download/release.json", 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := c.get(ctx, c.Base+"/latest/download/release.json.sig", 4096)
	if err != nil {
		return nil, err
	}
	m, err := Verify(c.PublicKey, manifest, sig)
	if err != nil {
		return nil, err
	}
	if !Newer(m.Version, current) {
		return nil, nil
	}
	name := AssetName(goarch)
	sum := strings.ToLower(m.Files[name])
	if len(sum) != 64 {
		return nil, fmt.Errorf("release %s has no %s", m.Version, name)
	}
	return &Release{Version: m.Version, URL: c.Base + "/download/" + m.Version + "/" + name, SHA256: sum}, nil
}

// Download fetches a release to dst and checks its hash. On any failure dst doesn't exist.
func (c *Checker) Download(ctx context.Context, r *Release, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", r.URL, nil)
	if err != nil {
		return err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", r.URL, res.StatusCode)
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxExe+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxExe {
		err = errors.New("the download is larger than any release should be")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		err = errors.New("the download doesn't match the signed release; not installing it")
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, dst)
}

// version is vMAJOR.MINOR.PATCH with an optional -prerelease.
type version struct {
	n   [3]int
	pre string
}

func parse(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(s, "v")
	s, v.pre, _ = strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}

// Newer reports whether release a is newer than the running version b. A build that isn't a
// release ("dev") never updates itself.
func Newer(a, b string) bool {
	va, ok1 := parse(a)
	vb, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va.n {
		if va.n[i] != vb.n[i] {
			return va.n[i] > vb.n[i]
		}
	}
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true // 1.2.0 is newer than 1.2.0-rc.1
	case vb.pre == "":
		return false
	}
	return va.pre > vb.pre
}
