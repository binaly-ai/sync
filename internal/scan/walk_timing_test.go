package scan

import (
	"os"
	"testing"
	"time"

	"github.com/binaly-ai/sync/internal/api"
)

// TestWalkTiming reports how long a walk of a real folder takes: the cost of each regular
// check. Set BINALY_SYNC_WALK_DIRS to one or more folders (separated by ";") to compare, say,
// the same folder through a local path and through a share.
func TestWalkTiming(t *testing.T) {
	dirs := os.Getenv("BINALY_SYNC_WALK_DIRS")
	if dirs == "" {
		t.Skip("set BINALY_SYNC_WALK_DIRS")
	}
	for _, d := range splitList(dirs) {
		for i := 0; i < 2; i++ { // the second walk shows the warm (cached) cost
			start := time.Now()
			entries, st, err := Walk(d, NewRules(api.DefaultPolicy, nil))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %d files (%d excluded) in %v", d, len(entries), st.Excluded, time.Since(start).Round(time.Millisecond))
		}
	}
}

func splitList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ';' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
