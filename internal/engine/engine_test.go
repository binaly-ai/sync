package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"context"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/auth"
	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/creds"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/state"
)

// fakeServer is a small stand-in for the Binaly server's /sync/v1 API: enough to check what
// the engine reports, uploads and deletes. It answers "upload" for content it hasn't stored.
type fakeServer struct {
	mu       sync.Mutex
	folder   string
	stored   map[string][]byte   // sha -> bytes
	reported map[string]string   // path key -> sha (live)
	gone     map[string]bool     // path key -> gone
	reports  []api.ReportIn
	srv      *httptest.Server
	// onReport, when set, runs after each report is stored (e.g. to pause the run mid-scan).
	onReport func(n int)
	// direct: "" offers no direct URL, "ok" a working one, "down" one nothing listens on.
	direct                 string
	directPuts, relayPuts  int
}

func newFake(t *testing.T, folder string) *fakeServer {
	f := &fakeServer{folder: folder, stored: map[string][]byte{}, reported: map[string]string{}, gone: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("authorization") != "Bearer test-token" && !strings.HasPrefix(r.URL.Path, "/put/") && !strings.HasPrefix(r.URL.Path, "/direct/") {
		w.WriteHeader(401)
		return
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/sync/v1/hello":
		reply(map[string]any{
			"machineId": "m_test", "workspace": map[string]string{"id": "w1", "name": "Test"},
			"policy":    api.DefaultPolicy,
			"folders":   []map[string]any{{"id": "f1", "path": f.folder, "exclusions": []string{}, "scanDue": true}},
		})
	case r.URL.Path == "/sync/v1/report":
		var in api.ReportIn
		json.NewDecoder(r.Body).Decode(&in)
		f.reports = append(f.reports, in)
		if f.onReport != nil {
			f.onReport(len(f.reports))
		}
		var results []api.FileResult
		for _, fr := range in.Files {
			k := pathx.Key(fr.Path)
			f.reported[k] = fr.SHA256
			delete(f.gone, k)
			res := api.FileResult{Path: fr.Path}
			switch {
			case fr.Size > api.DefaultPolicy.UploadLimitBytes:
				res.Action = "register"
			case f.stored[fr.SHA256] != nil:
				res.Action = "known"
			default:
				res.Action, res.UploadURL, res.StagingKey = "upload", f.srv.URL+"/put/"+fr.SHA256, "stage-"+fr.SHA256
				switch f.direct {
				case "ok":
					res.DirectURL, res.DirectHeaders = f.srv.URL+"/direct/"+fr.SHA256, map[string]string{"X-Signed": "yes"}
				case "down":
					res.DirectURL = "http://127.0.0.1:1/direct/" + fr.SHA256
				}
			}
			results = append(results, res)
		}
		for _, p := range in.Deleted {
			f.gone[pathx.Key(p)] = true
		}
		reply(map[string]any{"results": results})
	case strings.HasPrefix(r.URL.Path, "/direct/"):
		if r.Header.Get("X-Signed") != "yes" {
			w.WriteHeader(403) // the signature covers this header
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.directPuts++
		f.stored["pending:"+strings.TrimPrefix(r.URL.Path, "/direct/")] = b
		w.WriteHeader(200)
	case strings.HasPrefix(r.URL.Path, "/put/"):
		f.relayPuts++
		b, _ := io.ReadAll(r.Body)
		f.stored["pending:"+strings.TrimPrefix(r.URL.Path, "/put/")] = b
		w.WriteHeader(200)
	case r.URL.Path == "/sync/v1/commit":
		var in struct{ StagingKey, SHA256 string }
		json.NewDecoder(r.Body).Decode(&in)
		b := f.stored["pending:"+in.SHA256]
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != in.SHA256 {
			w.WriteHeader(409)
			reply(map[string]string{"error": "hash mismatch"})
			return
		}
		f.stored[in.SHA256] = b
		reply(map[string]any{"ok": true})
	case r.URL.Path == "/sync/v1/status":
		reply(map[string]any{"ok": true})
	case r.URL.Path == "/sync/v1/progress":
		reply(map[string]any{"overall": map[string]int{"total": len(f.reported)}})
	default:
		w.WriteHeader(404)
	}
}

func setup(t *testing.T) (*Engine, *fakeServer, string) {
	t.Setenv("BINALY_SYNC_HOME", t.TempDir())
	dir := t.TempDir()
	fake := newFake(t, dir)
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
	acct := config.Account{Server: fake.srv.URL, WorkspaceID: "w1", WorkspaceName: "Test", Static: true}
	auth.NewSource(acct.Key(), "", "", store).Save(auth.Secret{Static: "test-token"})
	cfg.PutAccount(acct)
	return &Engine{Cfg: cfg, State: st, Creds: store, Version: "test"}, fake, dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func filesReported(f *fakeServer, from int) []string {
	var out []string
	for _, r := range f.reports[from:] {
		for _, fr := range r.Files {
			out = append(out, filepath.Base(fr.Path))
		}
	}
	return out
}

func TestSyncCycle(t *testing.T) {
	e, fake, dir := setup(t)
	ctx := context.Background()
	write(t, filepath.Join(dir, "a.pdf"), "paper a")
	write(t, filepath.Join(dir, "copy.pdf"), "paper a")
	write(t, filepath.Join(dir, "sub", "b.csv"), "x,y\n1,2\n")
	write(t, filepath.Join(dir, "Thumbs.db"), "junk")
	write(t, filepath.Join(dir, ".git", "config"), "junk")

	// 1. First run: everything new is reported; identical content is uploaded once.
	if err := e.Run(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	got := filesReported(fake, 0)
	if len(got) != 3 {
		t.Fatalf("first run reported %v, want a.pdf, copy.pdf, b.csv", got)
	}
	for _, r := range got {
		if r == "Thumbs.db" || r == "config" {
			t.Fatalf("excluded file reported: %s", r)
		}
	}
	if len(fake.stored) != 2+2 { // 2 contents, each with its pending upload
		t.Fatalf("stored %d objects, want 2 distinct contents uploaded once each", len(fake.stored)/2)
	}
	if !fake.reports[0].Full || !fake.reports[0].First {
		t.Fatal("the first report of a new folder should open a full scan")
	}

	// 2. Nothing changed: nothing is reported (the full scan is done for the day).
	n := len(fake.reports)
	if err := e.Run(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := filesReported(fake, n); len(got) != 0 {
		t.Fatalf("unchanged run reported %v", got)
	}

	// 3. An edit, a new file and a deletion: exactly those are sent.
	write(t, filepath.Join(dir, "a.pdf"), "paper a, revised")
	write(t, filepath.Join(dir, "new.pdf"), "brand new")
	os.Remove(filepath.Join(dir, "sub", "b.csv"))
	n = len(fake.reports)
	if err := e.Run(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	got = filesReported(fake, n)
	if len(got) != 2 {
		t.Fatalf("change run reported %v, want a.pdf and new.pdf", got)
	}
	if !fake.gone[pathx.Key(filepath.Join(dir, "sub", "b.csv"))] {
		t.Fatal("the deleted file wasn't reported gone")
	}

	// 4. The folder disappears (a dropped drive): nothing is reported gone.
	os.Rename(dir, dir+"-away")
	defer os.Rename(dir+"-away", dir)
	n = len(fake.reports)
	if err := e.Run(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.reports[n:] {
		if len(r.Deleted) > 0 {
			t.Fatalf("an unreachable folder was reported as deletions: %v", r.Deleted)
		}
	}
	if s := e.Snapshot(); len(s.Workspaces) == 0 || len(s.Workspaces[0].Warnings) == 0 {
		t.Fatal("an unreachable folder should leave a warning")
	}
}

// A pause cancels the run. A full scan cut short has "not seen" most files, and must never
// report them as deleted, nor close the scan.
func TestPauseMidScanReportsNoDeletions(t *testing.T) {
	e, fake, dir := setup(t)
	for i := 0; i < 3*batchSize; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("f%04d.pdf", i)), fmt.Sprintf("paper %d", i))
	}
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	n := len(fake.reports)
	fake.onReport = func(count int) {
		if count == n+1 {
			cancel() // pause right after the first batch of a forced full scan
		}
	}
	if err := e.Run(ctx, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	fake.onReport = nil
	for _, r := range fake.reports[n:] {
		if len(r.Deleted) > 0 || r.Last {
			t.Fatalf("a paused scan reported deletions or closed the scan: %d deleted, last=%v", len(r.Deleted), r.Last)
		}
	}
	if got := len(fake.reports) - n; got > 1 {
		t.Fatalf("the run kept reporting after the pause: %d reports", got)
	}
	if s := e.Snapshot(); s.Activity != "Paused" {
		t.Fatalf("activity after a pause is %q, want Paused", s.Activity)
	}
}

// The upload queue never makes reporting wait, keeps order, and closes once drained.
func TestUnboundedQueue(t *testing.T) {
	in, out := unboundedQueue()
	const n = 50000 // far more than any fixed buffer the engine used to have
	for i := 0; i < n; i++ {
		in <- job{sha: fmt.Sprint(i)} // nobody is reading yet: this must not block
	}
	close(in)
	i := 0
	for j := range out {
		if j.sha != fmt.Sprint(i) {
			t.Fatalf("job %d came out as %s", i, j.sha)
		}
		i++
	}
	if i != n {
		t.Fatalf("got %d jobs, want %d", i, n)
	}
}

func TestUploadsGoDirectWhenOffered(t *testing.T) {
	e, fake, dir := setup(t)
	fake.direct = "ok"
	write(t, filepath.Join(dir, "a.pdf"), "paper a")
	write(t, filepath.Join(dir, "b.pdf"), "paper b")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if fake.directPuts != 2 || fake.relayPuts != 0 {
		t.Fatalf("direct %d, relay %d: want both files sent straight to storage", fake.directPuts, fake.relayPuts)
	}
	if fake.stored[sha("paper a")] == nil || fake.stored[sha("paper b")] == nil {
		t.Fatal("direct uploads were not committed")
	}
}

func TestUploadsFallBackToRelayWhenStorageUnreachable(t *testing.T) {
	e, fake, dir := setup(t)
	fake.direct = "down"
	write(t, filepath.Join(dir, "a.pdf"), "paper a")
	write(t, filepath.Join(dir, "b.pdf"), "paper b")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if fake.relayPuts != 2 {
		t.Fatalf("relay %d: want both files sent through the relay", fake.relayPuts)
	}
	if fake.stored[sha("paper a")] == nil || fake.stored[sha("paper b")] == nil {
		t.Fatal("relayed uploads were not committed")
	}
	if e.directDownUntil.IsZero() {
		t.Fatal("an unreachable storage host should switch later uploads to the relay")
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
