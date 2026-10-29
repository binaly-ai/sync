package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/binaly-ai/sync/internal/auth"
	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/creds"
	"github.com/binaly-ai/sync/internal/engine"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/state"
)

// treeServer answers hello, tree, marks and release the way the server does (its
// src/folder-tree.ts and sync-api.ts), and records what it was asked.
type treeServer struct {
	mu    sync.Mutex
	root  string // the synced folder, as the server knows it
	marks []map[string]any
	freed []string
	asked []string
}

func (s *treeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("content-type", "application/json")
	switch r.URL.Path {
	case "/sync/v1/hello":
		json.NewEncoder(w).Encode(map[string]any{
			"machineId": "m1",
			"policy":    map[string]any{"excludeNames": []string{"Thumbs.db"}, "excludeDirs": []string{".git", "OneNote_RecycleBin"}},
			"folders":   []map[string]any{{"id": "f1", "path": s.root, "exclusions": []string{}}},
		})
	case "/sync/v1/tree":
		p, _ := in["path"].(string)
		s.asked = append(s.asked, p)
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "path": p, "files": 3, "counts": map[string]int{"processed": 1, "waiting": 1, "sensitive": 1},
			"folders": []map[string]any{{"name": "OneNote_RecycleBin", "path": p + `\OneNote_RecycleBin`, "files": 2, "counts": map[string]int{"gone": 2}}, {
				"name": "Data", "path": p + `\Data`, "files": 2, "counts": map[string]int{"processed": 1, "waiting": 1},
				"mark": map[string]any{"mark": "first", "path": p + `\Data`, "own": true, "computer": "LAB-PC-2", "at": "2026-10-29T10:00:00Z"},
			}},
			"filesHere": []map[string]any{
				{"name": "grades.xlsx", "path": p + `\grades.xlsx`, "state": "sensitive", "notes": []string{}},
				{"name": "old.pdf", "path": p + `\old.pdf`, "state": "gone", "notes": []string{}},
			},
			"filesHereTotal": 2, "labels": map[string]string{"processed": "processed", "waiting": "waiting", "sensitive": "held", "gone": "removed"},
		})
	case "/sync/v1/marks":
		s.marks = append(s.marks, in)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "released": 2, "folders": []any{}})
	case "/sync/v1/release":
		p, _ := in["path"].(string)
		s.freed = append(s.freed, p)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "queued": []string{p + `\a`, p + `\b`}})
	default:
		w.WriteHeader(404)
	}
}

func treeApp(t *testing.T) (*trayApp, *treeServer, string, string) {
	t.Setenv("BINALY_SYNC_HOME", t.TempDir())
	disk := t.TempDir()
	synced := filepath.Join(disk, "data")
	for _, f := range []string{"data/Data/scan.csv", "data/grades.xlsx", "data/Thumbs.db", "data/New/x.txt", "Yossi/run.csv"} {
		p := filepath.Join(disk, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	fake := &treeServer{root: pathx.Canonical(synced)}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(config.Dir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	store := creds.Memory{}
	acct := config.Account{Server: srv.URL, WorkspaceID: "w1", WorkspaceName: "Test", Static: true}
	auth.NewSource(acct.Key(), "", "", store).Save(auth.Secret{Static: "test-token"})
	cfg.PutAccount(acct)
	e := &engine.Engine{Cfg: cfg, State: st, Creds: store, Version: "test"}
	app := &trayApp{e: e, trigger: make(chan engine.Options, 4), ctx: context.Background()}
	return app, fake, disk, acct.Key()
}

func TestTreeMergesThisComputerWithTheServer(t *testing.T) {
	app, fake, disk, key := treeApp(t)

	top, err := app.Tree(key, "")
	if err != nil {
		t.Fatal(err)
	}
	if v := top.(treeView); len(v.Synced) != 1 || len(v.Places) == 0 {
		t.Fatalf("top: %+v", v)
	}

	// The synced folder: local and server rows together, built-in junk hidden.
	out, err := app.Tree(key, filepath.Join(disk, "data"))
	if err != nil {
		t.Fatal(err)
	}
	v := out.(treeView)
	if v.Level == nil || v.SyncedRoot == "" || len(fake.asked) != 1 {
		t.Fatalf("no server level: %+v", v)
	}
	by := map[string]treeEntry{}
	for _, e := range v.Entries {
		by[e.Name] = e
	}
	if _, ok := by["OneNote_RecycleBin"]; ok {
		t.Error("a folder Sync never walks is listed because the server still has rows for it")
	}
	if _, ok := by["Thumbs.db"]; ok {
		t.Error("a built-in excluded file is listed")
	}
	if d := by["Data"]; !d.Dir || !d.Local || !d.Synced || d.Folder == nil || d.Folder.Mark == nil || d.Folder.Mark.Computer != "LAB-PC-2" {
		t.Errorf("Data: %+v", d)
	}
	if n := by["New"]; !n.Local || !n.Synced || n.Folder != nil {
		t.Errorf("a folder the server hasn't seen yet: %+v", n)
	}
	if g := by["grades.xlsx"]; g.File == nil || g.File.State != "sensitive" || !g.Local {
		t.Errorf("grades: %+v", g)
	}
	if o := by["old.pdf"]; o.Local || o.File == nil || o.File.State != "gone" {
		t.Errorf("a file only the server knows: %+v", o)
	}
	if v.Entries[0].Name != "Data" || !v.Entries[0].Dir {
		t.Errorf("folders come first: %v", v.Entries[0].Name)
	}

	// Outside any synced folder: this computer only, with what's synced and what isn't.
	out, err = app.Tree(key, disk)
	if err != nil {
		t.Fatal(err)
	}
	v = out.(treeView)
	if v.Level != nil || v.SyncedRoot != "" || len(fake.asked) != 1 {
		t.Fatalf("asked the server about an unsynced folder: %+v", v)
	}
	by = map[string]treeEntry{}
	for _, e := range v.Entries {
		by[e.Name] = e
	}
	if !by["data"].Synced || by["Yossi"].Synced || by["Yossi"].HoldsSynced {
		t.Errorf("synced and not synced: data %+v, Yossi %+v", by["data"], by["Yossi"])
	}
}

func TestTreeMarksAndReadIt(t *testing.T) {
	app, fake, disk, key := treeApp(t)
	target := filepath.Join(disk, "data", "Data")

	r, err := app.Mark(key, target, "first")
	if err != nil || r.(map[string]any)["released"] != 2 {
		t.Fatalf("mark: %v %v", r, err)
	}
	if len(app.trigger) != 0 {
		t.Error("Process first started a sync; only exclusions need one")
	}
	if _, err := app.Mark(key, target, "leave-out"); err != nil {
		t.Fatal(err)
	}
	if len(app.trigger) != 1 {
		t.Error("Leave out didn't start a sync, so this computer would keep sending the folder until the next one")
	}
	if len(fake.marks) != 2 || fake.marks[0]["mark"] != "first" || fake.marks[1]["mark"] != "leave-out" ||
		!strings.EqualFold(fake.marks[0]["path"].(string), pathx.Canonical(target)) || fake.marks[0]["machineId"] != "m1" {
		t.Fatalf("marks sent: %v", fake.marks)
	}
	q, err := app.Release(key, target)
	if err != nil || q.(map[string]any)["queued"] != 2 || len(fake.freed) != 1 {
		t.Fatalf("release: %v %v %v", q, err, fake.freed)
	}
	if _, err := app.Tree("nobody", ""); err == nil {
		t.Error("an unknown workspace was accepted")
	}
}
