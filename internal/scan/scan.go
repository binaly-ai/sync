// Package scan walks a folder and hashes what changed. Walking reads directory listings and
// compares each file's size and modification time with the cache; only new or changed files
// are opened, so a walk is cheap enough to repeat every few minutes.
package scan

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/pathx"
)

// Entry is one file found by a walk.
type Entry struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Stats counts what a walk left out.
type Stats struct {
	Excluded   int
	OnlineOnly int
	Unreadable int
}

// ErrRootUnreachable means the folder itself can't be read - a disconnected drive looks like
// this, and must never be reported as an empty folder.
var ErrRootUnreachable = errors.New("folder unreachable")

// Rules decide what a walk skips: the server's built-in exclusions and the folder's own.
type Rules struct {
	names   []string
	dirs    map[string]bool
	folder  [][]string
}

// NewRules builds the skip rules from the policy and a folder's exclusion patterns.
func NewRules(p api.Policy, folderExclusions []string) Rules {
	r := Rules{dirs: map[string]bool{}}
	for _, n := range p.ExcludeNames {
		r.names = append(r.names, strings.ToLower(n))
	}
	for _, d := range p.ExcludeDirs {
		r.dirs[strings.ToLower(d)] = true
	}
	for _, f := range folderExclusions {
		r.folder = append(r.folder, segments(f))
	}
	return r
}

func segments(p string) []string {
	parts := strings.Split(pathx.Key(p), "/")
	out := parts[:0]
	for i, s := range parts {
		if s != "" || i == 0 {
			out = append(out, s)
		}
	}
	return out
}

// matchesFolderRule mirrors the server's pattern rule: a pattern names a folder or an
// ancestor of it, and "*" matches any one segment.
func (r Rules) matchesFolderRule(path string) bool {
	seg := segments(path)
outer:
	for _, pat := range r.folder {
		if len(pat) > len(seg) {
			continue
		}
		for i := range pat {
			if pat[i] != "*" && pat[i] != seg[i] {
				continue outer
			}
		}
		return true
	}
	return false
}

// SkipFolderRule reports whether a path falls under one of the folder's own exclusions.
func (r Rules) SkipFolderRule(path string) bool { return r.matchesFolderRule(path) }

func (r Rules) skipName(name string) bool {
	n := strings.ToLower(name)
	for _, g := range r.names {
		if ok, _ := filepath.Match(g, n); ok {
			return true
		}
	}
	return false
}

// Walk lists the files under root that sync should consider.
func Walk(root string, r Rules) ([]Entry, Stats, error) {
	var out []Entry
	var st Stats
	if svc := CloudDrive(root); svc != "" {
		return nil, st, fmt.Errorf("%w: %s", ErrCloudDrive, svc)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, st, fmt.Errorf("%w: %s", ErrRootUnreachable, root)
	}
	// A root we can't list at all (a share that just dropped) is unreachable, not empty.
	if _, err := os.ReadDir(root); err != nil {
		return nil, st, fmt.Errorf("%w: %v", ErrRootUnreachable, err)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			st.Unreadable++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && (r.dirs[strings.ToLower(d.Name())] || r.matchesFolderRule(p)) {
				st.Excluded++
				return fs.SkipDir
			}
			return nil
		}
		// Symlinks and junctions are not followed (WalkDir reports them as non-regular files).
		if !d.Type().IsRegular() {
			return nil
		}
		if r.skipName(d.Name()) || r.matchesFolderRule(p) {
			st.Excluded++
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			st.Unreadable++
			return nil
		}
		// A cloud placeholder downloads in full the moment anything reads it.
		if onlineOnly(fi) {
			st.OnlineOnly++
			return nil
		}
		out = append(out, Entry{Path: p, Size: fi.Size(), ModTime: fi.ModTime()})
		return nil
	})
	if err != nil {
		return nil, st, fmt.Errorf("%w: %v", ErrRootUnreachable, err)
	}
	return out, st, nil
}

// ErrCloudDrive means the folder is on a cloud drive whose files download in full when read
// (Google Drive for desktop). Walking it would quietly download the whole drive.
var ErrCloudDrive = errors.New("folder is on a cloud drive, which Binaly Sync doesn't sync yet")

// ErrChanging means the file changed while it was being hashed; try again next run.
var ErrChanging = errors.New("file changed while reading")

// Hash returns the SHA-256 of the whole file (the same hash the server computes on upload).
// Size and modification time are checked before and after, so a file being written is
// skipped rather than reported with a hash of half its bytes.
func Hash(e Entry) (string, error) {
	f, err := os.Open(e.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	after, err := os.Stat(e.Path)
	if err != nil {
		return "", err
	}
	if after.Size() != e.Size || !after.ModTime().Equal(e.ModTime) {
		return "", ErrChanging
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var sharedString = regexp.MustCompile(`<t[^>]*>([^<]*)</t>`)

// Preview returns the first bytes of a text data file, or the first header cells of an
// .xlsx. Only for the types the policy names; the server has named none since 2026-10-25.
func Preview(path string, p api.Policy) string {
	ext := strings.ToLower(filepath.Ext(path))
	wanted := false
	for _, x := range p.PreviewExtensions {
		if strings.EqualFold(x, ext) {
			wanted = true
		}
	}
	if !wanted || p.PreviewBytes <= 0 {
		return ""
	}
	if ext == ".xlsx" {
		z, err := zip.OpenReader(path)
		if err != nil {
			return ""
		}
		defer z.Close()
		for _, f := range z.File {
			if f.Name != "xl/sharedStrings.xml" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			raw, _ := io.ReadAll(io.LimitReader(rc, 256<<10))
			rc.Close()
			var cells []string
			n := 0
			for _, m := range sharedString.FindAllStringSubmatch(string(raw), 200) {
				if n += len(m[1]) + 1; n > p.PreviewBytes {
					break
				}
				cells = append(cells, m[1])
			}
			return strings.Join(cells, ",")
		}
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, p.PreviewBytes)
	n, _ := io.ReadFull(f, buf)
	s := string(buf[:n])
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return s
}
