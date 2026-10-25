package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWatchOnANetworkShare runs the watching scenario on a real share, which is where change
// notices are least reliable. Set BINALY_SYNC_SHARE_DIR to a folder on one (a mapped drive or a
// \server\share path); the test works in a new subfolder and removes it.
func TestWatchOnANetworkShare(t *testing.T) {
	root := os.Getenv("BINALY_SYNC_SHARE_DIR")
	if root == "" {
		t.Skip("set BINALY_SYNC_SHARE_DIR to a folder on a network share")
	}
	dir := filepath.Join(root, "watch-test-"+time.Now().Format("150405"))
	sub := filepath.Join(dir, "Samples", "DV10")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	w := New()
	defer w.Close()
	w.Set([]Folder{{Key: "share", Path: dir, Ignore: func(rel string) bool { return strings.HasPrefix(filepath.Base(rel), "~$") }}})
	if len(w.Watching()) != 1 {
		t.Fatal("the share can't be watched")
	}
	os.WriteFile(filepath.Join(sub, "~$results.docx"), []byte("x"), 0o644)
	if got := waitChange(w, 2*time.Second); got != nil {
		t.Fatalf("a lock file started a sync: %v", got)
	}
	start := time.Now()
	os.WriteFile(filepath.Join(sub, "run-12.csv"), []byte("a,b\n1,2\n"), 0o644)
	if got := waitChange(w, 10*time.Second); len(got) != 1 {
		t.Fatalf("a new file on the share: got %v", got)
	}
	t.Logf("new file noticed after %v", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	os.Rename(filepath.Join(sub, "run-12.csv"), filepath.Join(sub, "run-12-final.csv"))
	if got := waitChange(w, 10*time.Second); len(got) != 1 {
		t.Fatalf("a rename on the share: got %v", got)
	}
	t.Logf("rename noticed after %v", time.Since(start).Round(time.Millisecond))
}
