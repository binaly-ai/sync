// Package engine runs one sync: check in with every signed-in workspace, walk each folder once,
// hash what changed (smallest files first), report it, upload what the server asks for, and
// tell each workspace what's gone. The server decides everything; the engine reports and obeys.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/auth"
	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/creds"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/runlog"
	"github.com/binaly-ai/sync/internal/scan"
	"github.com/binaly-ai/sync/internal/state"
	"github.com/binaly-ai/sync/internal/upload"
)

const (
	batchSize     = 200
	uploadWorkers = 3
	// A workspace gets a full report (every file) at least this often, so its server-side
	// sweep catches anything an incremental run missed.
	fullEvery = 24 * time.Hour
	// How often a run refreshes the bar from the server while it reports and uploads.
	progressRefresh = 15 * time.Second
)

// Options shape one run.
type Options struct {
	// Workspace limits the run to one workspace (name or id); empty means all.
	Workspace string
	// Force walks every folder even if another machine walked it recently.
	Force bool
	// ConfirmMassDeletion passes the server's mass-deletion guard (a person checked).
	ConfirmMassDeletion bool
}

// Engine is the long-lived sync machinery. One per process.
type Engine struct {
	Cfg     *config.Store
	State   *state.DB
	Creds   creds.Store
	Version string

	mu     sync.Mutex
	status Status
	metas  map[string]auth.Meta
}

// Status is what the tray and the status page show.
type Status struct {
	Running    bool              `json:"running"`
	Activity   string            `json:"activity"`
	LastRunAt  string            `json:"lastRunAt,omitempty"`
	LastLog    string            `json:"lastLog,omitempty"`
	Workspaces []WorkspaceStatus `json:"workspaces"`
	Uploading  struct {
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	} `json:"uploading"`
}

// WorkspaceStatus is one signed-in workspace.
type WorkspaceStatus struct {
	Key      string               `json:"key"`
	Name     string               `json:"name"`
	Email    string               `json:"email,omitempty"`
	Server   string               `json:"server"`
	State    string               `json:"state"` // ok | needs-sign-in | revoked | error
	Error    string               `json:"error,omitempty"`
	Progress *api.Progress        `json:"progress,omitempty"`
	Folders  []api.FolderProgress `json:"folders,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
}

// Snapshot returns the current status.
func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status
	s.Workspaces = append([]WorkspaceStatus(nil), e.status.Workspaces...)
	return s
}

// ForgetFolder drops a folder from the status right away, once the server has confirmed this
// computer stopped syncing it, instead of leaving it listed until the next refresh.
func (e *Engine) ForgetFolder(accountKey, folderID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.status.Workspaces {
		w := &e.status.Workspaces[i]
		if w.Key != accountKey {
			continue
		}
		kept := make([]api.FolderProgress, 0, len(w.Folders))
		for _, f := range w.Folders {
			if f.ID != folderID {
				kept = append(kept, f)
			}
		}
		w.Folders = kept
	}
}

func (e *Engine) setActivity(format string, a ...any) {
	e.mu.Lock()
	e.status.Activity = fmt.Sprintf(format, a...)
	e.mu.Unlock()
}

// Meta returns (and caches) a server's OAuth endpoints.
func (e *Engine) Meta(ctx context.Context, server string) (auth.Meta, error) {
	e.mu.Lock()
	if e.metas == nil {
		e.metas = map[string]auth.Meta{}
	}
	m, ok := e.metas[server]
	e.mu.Unlock()
	if ok {
		return m, nil
	}
	m, err := auth.Discover(ctx, server)
	if err != nil {
		return m, err
	}
	e.mu.Lock()
	e.metas[server] = m
	e.mu.Unlock()
	return m, nil
}

// Client builds the API client for an account.
func (e *Engine) Client(ctx context.Context, a config.Account) *api.Client {
	tokenURL := ""
	if !a.Static {
		if m, err := e.Meta(ctx, a.Server); err == nil {
			tokenURL = m.TokenEndpoint
		}
	}
	return &api.Client{Server: a.Server, Tokens: auth.NewSource(a.Key(), a.ClientID, tokenURL, e.Creds)}
}

// MachineName is how this machine appears in Binaly.
func MachineName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "this computer"
}

// Hello checks in with one account and records what this workspace calls this machine.
func (e *Engine) Hello(ctx context.Context, a config.Account) (*api.HelloOut, error) {
	c := e.Client(ctx, a)
	out, err := c.Hello(ctx, api.HelloIn{
		InstallID:   e.Cfg.InstallID(),
		MachineName: MachineName(),
		OS:          runtime.GOOS,
		Version:     e.Version,
		DriveMap:    pathx.DriveMap(),
	})
	if err != nil {
		return nil, err
	}
	changed := false
	if out.MachineID != "" && out.MachineID != a.MachineID {
		a.MachineID, changed = out.MachineID, true
	}
	if out.Workspace.Name != "" && out.Workspace.Name != a.WorkspaceName {
		a.WorkspaceName, changed = out.Workspace.Name, true
	}
	if out.User != nil && out.User.Email != "" && out.User.Email != a.Email {
		a.Email, changed = out.User.Email, true
	}
	if changed {
		_ = e.Cfg.PutAccount(a)
	}
	return out, nil
}

// one workspace's view of one folder during a run
type target struct {
	acct    config.Account
	client  *api.Client
	policy  api.Policy
	folder  api.Folder
	rules   scan.Rules
	full    bool
	scanID  string
	first   bool
	report  map[string]state.Reported // what it was told before this run
	seen    map[string]bool
	ws      *WorkspaceStatus
}

type job struct {
	t      *target
	entry  scan.Entry
	key    string
	sha    string
	url    string
	stage  string
}

// Run performs one sync across every signed-in workspace.
func (e *Engine) Run(ctx context.Context, opt Options) error {
	if paused, until := e.Cfg.Settings().Paused(time.Now()); paused {
		if until.IsZero() {
			e.setActivity("Paused until you resume")
		} else {
			e.setActivity("Paused until %s", until.Local().Format("Mon 15:04"))
		}
		return nil
	}
	e.mu.Lock()
	if e.status.Running {
		e.mu.Unlock()
		return errors.New("a sync is already running")
	}
	e.status.Running = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.status.Running = false
		e.status.LastRunAt = time.Now().Format(time.RFC3339)
		e.mu.Unlock()
	}()

	lg, _ := runlog.Start(config.Dir())
	defer lg.Close()
	if lg != nil {
		e.mu.Lock()
		e.status.LastLog = lg.Path
		e.mu.Unlock()
	}
	started := time.Now()

	// 1. Check in with every workspace.
	var statuses []WorkspaceStatus
	byFolder := map[string][]*target{} // folder path key -> every workspace syncing it
	folderPath := map[string]string{}
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	var runs []*acctRun
	accounts := filterAccounts(e.Cfg.Accounts(), opt.Workspace)
	for _, a := range accounts {
		statuses = append(statuses, WorkspaceStatus{Key: a.Key(), Name: a.WorkspaceName, Email: a.Email, Server: a.Server, State: "ok"})
	}
	for i, a := range accounts {
		ws := &statuses[i]
		e.setActivity("Checking in with %s", a.WorkspaceName)
		h, err := e.Hello(ctx, a)
		if err != nil {
			ws.State, ws.Error = classify(err)
			lg.Event("hello-failed", map[string]any{"workspace": a.WorkspaceName, "error": err.Error()})
			continue
		}
		a.MachineID = h.MachineID
		ws.Name = h.Workspace.Name
		p := h.Progress
		ws.Progress = &p
		r := &acctRun{acct: a, client: e.Client(ctx, a), ws: ws, counts: map[string]int{}}
		runs = append(runs, r)
		lg.Event("hello", map[string]any{"workspace": ws.Name, "policy": h.Policy.Version, "folders": len(h.Folders)})
		for _, f := range h.Folders {
			if f.Warning != nil {
				ws.Warnings = append(ws.Warnings, f.Warning.Reason)
			}
			key := pathx.Key(f.Path)
			hasReported := e.State.HasReported(a.Key(), f.ID)
			full := opt.Force || !hasReported || time.Since(e.State.LastFull(a.Key(), f.ID)) > fullEvery
			if !f.ScanDue && !full && !opt.Force {
				continue // another machine walked it moments ago
			}
			reported, _ := e.State.AllReported(a.Key(), f.ID)
			byFolder[key] = append(byFolder[key], &target{
				acct: a, client: r.client, policy: h.Policy, folder: f, rules: scan.NewRules(h.Policy, f.Exclusions),
				full: full, scanID: runID + "-" + f.ID, first: true, report: reported, seen: map[string]bool{}, ws: ws,
			})
			folderPath[key] = f.Path
		}
	}
	e.mu.Lock()
	e.status.Workspaces = statuses
	e.mu.Unlock()

	// The bar and the folder list come from the server, which counts files as they're reported
	// and uploaded. A first sync of a big folder is one long run, so refresh them during it,
	// not only at the end: otherwise the window says "Counting files..." while it uploads.
	refreshDone := make(chan struct{})
	go func() {
		tick := time.NewTicker(progressRefresh)
		defer tick.Stop()
		for {
			select {
			case <-refreshDone:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				e.refreshProgress(ctx, runs)
			}
		}
	}()

	// 2. Upload workers, fed as reports come back. The queue has no fixed size: reporting must
	// never wait for uploads, or a folder with more files to send than the queue held couldn't
	// finish its first look (and show its total) until most of the uploading was done.
	lim := upload.NewLimiter(e.Cfg.Settings().BandwidthKBps)
	queued, jobs := unboundedQueue()
	var wg sync.WaitGroup
	uploaded, uploadFailed := 0, 0
	for i := 0; i < uploadWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					// Paused or quitting: drop the rest quietly. Each file is still marked as
					// not uploaded, so the next run sends it.
					e.mu.Lock()
					e.status.Uploading.Files--
					e.status.Uploading.Bytes -= j.entry.Size
					e.mu.Unlock()
					continue
				}
				err := e.uploadOne(ctx, j, lim)
				e.mu.Lock()
				e.status.Uploading.Files--
				e.status.Uploading.Bytes -= j.entry.Size
				if err != nil && ctx.Err() != nil {
					// Cut off mid-upload by a pause: not a failure.
				} else if err != nil {
					uploadFailed++
					lg.Event("upload-failed", map[string]any{"path": j.entry.Path, "error": err.Error()})
				} else {
					uploaded++
					lg.Event("uploaded", map[string]any{"path": j.entry.Path, "sha256": j.sha, "size": j.entry.Size, "workspace": j.t.ws.Name})
				}
				e.mu.Unlock()
			}
		}()
	}

	// 3. Walk each folder once, for every workspace that syncs it.
	keys := make([]string, 0, len(byFolder))
	for k := range byFolder {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		targets := byFolder[key]
		root := folderPath[key]
		e.setActivity("Looking through %s", root)
		entries, st, err := scan.Walk(localPath(root), scan.NewRules(targets[0].policy, nil))
		if err != nil {
			// Unreachable is not empty: report nothing, so nothing is marked gone.
			for _, t := range targets {
				t.ws.Warnings = append(t.ws.Warnings, fmt.Sprintf("%s couldn't be read this run (%v)", root, err))
			}
			lg.Event("folder-unreachable", map[string]any{"folder": root, "error": err.Error()})
			continue
		}
		lg.Event("walked", map[string]any{"folder": root, "files": len(entries), "excluded": st.Excluded, "onlineOnly": st.OnlineOnly, "unreadable": st.Unreadable})
		e.syncFolder(ctx, root, entries, targets, queued, lg, opt)
	}
	close(queued)
	wg.Wait()
	close(refreshDone)

	// 4. Tell each workspace how the run went, and refresh its bar.
	for _, r := range runs {
		r.counts["uploaded"] = uploaded
		r.counts["uploadFailed"] = uploadFailed
		_ = r.client.Status(ctx, api.StatusIn{
			MachineID: r.acct.MachineID, OK: uploadFailed == 0, DurationMs: time.Since(started).Milliseconds(),
			Counts: r.counts, Errors: r.ws.Warnings, Unsynced: unsyncedSiblings(byFolder),
		})
	}
	e.refreshProgress(ctx, runs)
	e.mu.Lock()
	e.status.Workspaces = statuses
	e.mu.Unlock()
	if ctx.Err() != nil {
		e.setActivity("Paused")
		lg.Event("stopped", map[string]any{"uploaded": uploaded, "seconds": int(time.Since(started).Seconds())})
		return nil
	}
	e.setActivity("Up to date")
	lg.Event("done", map[string]any{"uploaded": uploaded, "uploadFailed": uploadFailed, "seconds": int(time.Since(started).Seconds())})
	return nil
}

func filterAccounts(all []config.Account, ws string) []config.Account {
	var out []config.Account
	for _, a := range all {
		if ws == "" || strings.EqualFold(ws, a.WorkspaceName) || ws == a.WorkspaceID {
			out = append(out, a)
		}
	}
	return out
}

func classify(err error) (string, string) {
	switch {
	case errors.Is(err, auth.ErrNeedsSignIn):
		return "needs-sign-in", "Sign in again to keep syncing this workspace."
	case errors.Is(err, api.ErrRevoked):
		return "revoked", "This computer was disconnected from the workspace in Binaly."
	default:
		return "error", err.Error()
	}
}

// localPath turns a reported network path back into something this machine can open. The
// network path itself works on Windows; this is the hook for anything that doesn't.
func localPath(p string) string { return p }

// syncFolder hashes, reports and queues uploads for one walked folder, for each workspace.
func (e *Engine) syncFolder(ctx context.Context, root string, entries []scan.Entry, targets []*target, jobs chan<- job, lg *runlog.Log, opt Options) {
	// Smallest first: the queue fills with readable files soonest, and uploads follow suit.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Size < entries[j].Size })
	for start := 0; start < len(entries); start += batchSize {
		end := start + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		// Paused (or quitting): stop here. Returning before the deletions step below matters - a
		// walk cut short has "not seen" most files, and must never report them as deleted.
		if ctx.Err() != nil {
			return
		}
		chunk := entries[start:end]
		e.setActivity("Checking files in %s (%d of %d)", root, end, len(entries))

		// Hash what the cache doesn't know (once, whatever the number of workspaces).
		hashes := map[string]string{}
		fresh := map[string]state.Hash{}
		for _, en := range chunk {
			if ctx.Err() != nil {
				return
			}
			key := pathx.Key(pathx.Canonical(en.Path))
			if sha, ok := e.State.CachedHash(key, en.Size, en.ModTime.UnixNano()); ok {
				hashes[key] = sha
				continue
			}
			sha, err := scan.Hash(en)
			if err != nil {
				lg.Event("hash-skipped", map[string]any{"path": en.Path, "error": err.Error()})
				continue // locked or changing: next run
			}
			hashes[key] = sha
			fresh[key] = state.Hash{Size: en.Size, MtimeNs: en.ModTime.UnixNano(), SHA256: sha}
		}
		_ = e.State.PutHashes(fresh)

		for _, t := range targets {
			var files []api.FileReport
			byPath := map[string]scan.Entry{}
			for _, en := range chunk {
				p := pathx.Canonical(en.Path)
				key := pathx.Key(p)
				sha, ok := hashes[key]
				if !ok {
					t.seen[key] = true // couldn't hash: don't treat as deleted
					continue
				}
				if t.rules.SkipFolderRule(p) {
					continue
				}
				t.seen[key] = true
				prev, had := t.report[key]
				if !t.full && had && prev.Uploaded && prev.SHA256 == sha && prev.Size == en.Size && prev.MtimeNs == en.ModTime.UnixNano() {
					continue // unchanged and fully synced: nothing to say
				}
				files = append(files, api.FileReport{Path: p, Size: en.Size, Mtime: en.ModTime.UTC().Format(time.RFC3339Nano), SHA256: sha, Preview: scan.Preview(en.Path, t.policy)})
				byPath[key] = en
			}
			if len(files) == 0 && !(t.full && t.first) {
				continue
			}
			out, err := t.client.Report(ctx, api.ReportIn{
				MachineID: t.acct.MachineID, FolderID: t.folder.ID, ScanID: t.scanID, Files: files,
				Full: t.full, First: t.first,
			})
			if err != nil {
				t.ws.Warnings = append(t.ws.Warnings, fmt.Sprintf("report for %s failed: %v", root, err))
				lg.Event("report-failed", map[string]any{"folder": root, "error": err.Error()})
				continue
			}
			t.first = false
			e.applyResults(ctx, t, out, byPath, hashes, jobs, lg)
		}
	}

	// Deletions, then close the scan - only after a walk that ran to the end.
	if ctx.Err() != nil {
		return
	}
	for _, t := range targets {
		var deleted []string
		var deletedKeys []string
		for key, r := range t.report {
			if !t.seen[key] {
				deleted = append(deleted, r.Path)
				deletedKeys = append(deletedKeys, key)
			}
		}
		for i := 0; ; i += 1000 {
			end := i + 1000
			if end > len(deleted) {
				end = len(deleted)
			}
			last := end >= len(deleted)
			out, err := t.client.Report(ctx, api.ReportIn{
				MachineID: t.acct.MachineID, FolderID: t.folder.ID, ScanID: t.scanID, Files: []api.FileReport{},
				Deleted: deleted[i:end], Full: t.full, First: t.first, Last: last, ConfirmMassDeletion: opt.ConfirmMassDeletion,
			})
			t.first = false
			if err != nil {
				t.ws.Warnings = append(t.ws.Warnings, fmt.Sprintf("report for %s failed: %v", root, err))
				break
			}
			if out.Warning != "" {
				// The guard held: keep remembering these paths; the next run tells again.
				t.ws.Warnings = append(t.ws.Warnings, out.Warning)
				lg.Event("deletions-held", map[string]any{"folder": root, "warning": out.Warning})
			} else {
				_ = e.State.DeleteReported(t.acct.Key(), t.folder.ID, deletedKeys[i:end])
				if end > i {
					lg.Event("gone", map[string]any{"folder": root, "count": end - i})
				}
			}
			if last {
				break
			}
		}
		if t.full {
			_ = e.State.SetLastFull(t.acct.Key(), t.folder.ID, time.Now())
		}
		// An empty folder still needs remembering, so the next run isn't treated as a first.
		_ = e.State.PutReported(t.acct.Key(), t.folder.ID, nil)
	}
}

func (e *Engine) applyResults(ctx context.Context, t *target, out *api.ReportOut, byPath map[string]scan.Entry, hashes map[string]string, jobs chan<- job, lg *runlog.Log) {
	rec := map[string]state.Reported{}
	for _, r := range out.Results {
		key := pathx.Key(r.Path)
		en, ok := byPath[key]
		if !ok {
			continue
		}
		entry := state.Reported{Path: r.Path, Size: en.Size, MtimeNs: en.ModTime.UnixNano(), SHA256: hashes[key], Uploaded: true}
		switch r.Action {
		case "upload":
			entry.Uploaded = false // until the commit lands
			e.mu.Lock()
			e.status.Uploading.Files++
			e.status.Uploading.Bytes += en.Size
			e.mu.Unlock()
			jobs <- job{t: t, entry: en, key: key, sha: hashes[key], url: r.UploadURL, stage: r.StagingKey}
		case "register", "known":
			lg.Event(r.Action, map[string]any{"path": r.Path, "workspace": t.ws.Name})
		default:
			lg.Event(r.Action, map[string]any{"path": r.Path, "reason": r.Reason, "workspace": t.ws.Name})
		}
		rec[key] = entry
	}
	_ = e.State.PutReported(t.acct.Key(), t.folder.ID, rec)
}

// unboundedQueue returns a send side that never blocks and a receive side the upload workers
// range over. Closing the send side closes the receive side once everything queued is taken.
func unboundedQueue() (chan<- job, <-chan job) {
	in, out := make(chan job), make(chan job)
	go func() {
		defer close(out)
		var q []job
		for in != nil || len(q) > 0 {
			var send chan job
			var next job
			if len(q) > 0 {
				send, next = out, q[0]
			}
			select {
			case j, ok := <-in:
				if !ok {
					in = nil
					continue
				}
				q = append(q, j)
			case send <- next:
				q[0] = job{} // let the sent job be collected
				q = q[1:]
			}
		}
	}()
	return in, out
}

// acctRun is one workspace's part of a run.
type acctRun struct {
	acct   config.Account
	client *api.Client
	ws     *WorkspaceStatus
	counts map[string]int
	errs   []string
}

// refreshProgress asks each workspace for its bar and folder list. A failed request leaves the
// last numbers in place; the next refresh tries again.
func (e *Engine) refreshProgress(ctx context.Context, runs []*acctRun) {
	for _, r := range runs {
		pr, err := r.client.GetProgress(ctx)
		if err != nil {
			continue
		}
		p := pr.Overall
		e.mu.Lock()
		r.ws.Progress = &p
		r.ws.Folders = pr.Folders
		e.mu.Unlock()
	}
}

func (e *Engine) uploadOne(ctx context.Context, j job, lim *upload.Limiter) error {
	// The file must still be what was hashed; otherwise the next run reports the new version.
	fi, err := os.Stat(j.entry.Path)
	if err != nil {
		return err
	}
	if fi.Size() != j.entry.Size || !fi.ModTime().Equal(j.entry.ModTime) {
		return scan.ErrChanging
	}
	e.setActivity("Uploading %s", filepath.Base(j.entry.Path))
	if err := upload.Put(ctx, j.url, j.entry.Path, j.entry.Size, lim); err != nil {
		return err
	}
	p := pathx.Canonical(j.entry.Path)
	if err := j.t.client.Commit(ctx, j.t.acct.MachineID, j.stage, p, j.sha); err != nil {
		return err
	}
	return e.State.PutReported(j.t.acct.Key(), j.t.folder.ID, map[string]state.Reported{
		j.key: {Path: p, Size: j.entry.Size, MtimeNs: j.entry.ModTime.UnixNano(), SHA256: j.sha, Uploaded: true},
	})
}

// unsyncedSiblings lists folders next to synced ones that nothing syncs ("Not syncing" in the app).
func unsyncedSiblings(byFolder map[string][]*target) []string {
	synced := map[string]bool{}
	parents := map[string]bool{}
	for _, ts := range byFolder {
		for _, t := range ts {
			synced[pathx.Key(t.folder.Path)] = true
			parents[filepath.Dir(t.folder.Path)] = true
		}
	}
	var out []string
	for parent := range parents {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, en := range entries {
			if !en.IsDir() || strings.HasPrefix(en.Name(), ".") || strings.HasPrefix(en.Name(), "$") {
				continue
			}
			p := filepath.Join(parent, en.Name())
			if !synced[pathx.Key(p)] {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}
