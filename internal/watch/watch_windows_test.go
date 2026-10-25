package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitChange returns the folders that changed within d, or nil. A signal with nothing to take
// is left over from a burst already taken (the tray app skips those too), so it keeps waiting.
func waitChange(w *Watcher, d time.Duration) []string {
	deadline := time.After(d)
	for {
		select {
		case <-w.Changed():
			time.Sleep(200 * time.Millisecond) // let the rest of the burst land
			if got := w.Take(); len(got) > 0 {
				return got
			}
		case <-deadline:
			return nil
		}
	}
}

func TestWatchSeesNewFilesAndIgnoresNoise(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Samples", "DV10")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ignore := func(rel string) bool {
		base := filepath.Base(rel)
		return strings.HasPrefix(base, "~$") || strings.EqualFold(base, "Thumbs.db")
	}
	w := New()
	defer w.Close()
	w.Set([]Folder{{Key: "lab", Path: dir, Ignore: ignore}})
	if got := w.Watching(); len(got) != 1 {
		t.Fatalf("watching %v, want the one folder", got)
	}

	// Noise: an Office lock file and a thumbnail cache, deep inside the folder.
	os.WriteFile(filepath.Join(sub, "~$results.docx"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(sub, "Thumbs.db"), []byte("x"), 0o644)
	if got := waitChange(w, 1500*time.Millisecond); got != nil {
		t.Fatalf("noise started a sync: %v", got)
	}

	// A real result.
	os.WriteFile(filepath.Join(sub, "run-12.csv"), []byte("a,b\n1,2\n"), 0o644)
	if got := waitChange(w, 5*time.Second); len(got) != 1 || got[0] != "lab" {
		t.Fatalf("a new file: got %v, want [lab]", got)
	}

	// A deletion counts too.
	os.Remove(filepath.Join(sub, "run-12.csv"))
	if got := waitChange(w, 5*time.Second); len(got) != 1 {
		t.Fatalf("a deletion: got %v, want [lab]", got)
	}

	// Stopped means stopped.
	w.Set(nil)
	time.Sleep(200 * time.Millisecond)
	os.WriteFile(filepath.Join(sub, "run-13.csv"), []byte("x"), 0o644)
	if got := waitChange(w, 1500*time.Millisecond); got != nil {
		t.Fatalf("a stopped watcher reported %v", got)
	}
}

func TestWatchDropsAFolderThatGoesAway(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "share")
	os.MkdirAll(dir, 0o755)
	w := New()
	defer w.Close()
	w.Set([]Folder{{Key: "share", Path: dir}})
	if err := os.RemoveAll(dir); err != nil {
		t.Skip("can't remove a watched folder here:", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(w.Watching()) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := w.Watching(); len(got) != 0 {
		t.Fatalf("still watching a folder that's gone: %v", got)
	}
	// It comes back once the folder does.
	os.MkdirAll(dir, 0o755)
	w.Set([]Folder{{Key: "share", Path: dir}})
	if got := w.Watching(); len(got) != 1 {
		t.Fatalf("not watching the folder again: %v", got)
	}
}

func TestWatchSkipsAMissingFolder(t *testing.T) {
	w := New()
	defer w.Close()
	w.Set([]Folder{{Key: "gone", Path: filepath.Join(t.TempDir(), "nope")}})
	if got := w.Watching(); len(got) != 0 {
		t.Fatalf("watching a folder that doesn't exist: %v", got)
	}
}
