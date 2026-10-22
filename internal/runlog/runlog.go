// Package runlog writes one JSON-lines file per run: every file that left the machine, every
// decision the server made, and every error - so anyone (an IT reviewer above all) can see
// exactly what a run sent, under which policy version. The last 30 runs are kept.
package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Log is one run's log file.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	Path string
}

// Dir is where run logs go.
func Dir(base string) string { return filepath.Join(base, "logs") }

// Start opens a new run log under base and prunes old ones.
func Start(base string) (*Log, error) {
	dir := Dir(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	prune(dir, 30)
	p := filepath.Join(dir, "run-"+time.Now().Format("20060102-150405.000")+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{f: f, Path: p}, nil
}

// Event writes one line. A nil Log discards.
func (l *Log) Event(kind string, fields map[string]any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["t"] = time.Now().UTC().Format(time.RFC3339)
	fields["event"] = kind
	b, _ := json.Marshal(fields)
	l.f.Write(append(b, '\n'))
}

// Close closes the file.
func (l *Log) Close() {
	if l != nil {
		l.f.Close()
	}
}

func prune(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) >= keep {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}
