package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/sqweek/dialog"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/engine"
	"github.com/binaly-ai/sync/internal/install"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/runlog"
	"github.com/binaly-ai/sync/internal/scan"
	"github.com/binaly-ai/sync/internal/ui"
)

var (
	//go:embed icons/binaly.ico
	iconNormal []byte
	//go:embed icons/attention.ico
	iconAttention []byte
	//go:embed icons/paused.ico
	iconPaused []byte
	//go:embed icons/binaly-dark.ico
	iconNormalDark []byte
	//go:embed icons/attention-dark.ico
	iconAttentionDark []byte
	//go:embed icons/paused-dark.ico
	iconPausedDark []byte
)

// trayIcons picks the artwork that shows up against the taskbar: the brand navy on a light
// taskbar, the dark-theme artwork on a dark one.
func trayIcons() (normal, attention, paused []byte) {
	if taskbarDark() {
		return iconNormalDark, iconAttentionDark, iconPausedDark
	}
	return iconNormal, iconAttention, iconPaused
}

// trayApp is the running tray app, and the backend its window calls.
type trayApp struct {
	e       *engine.Engine
	win     *ui.Window
	trigger chan engine.Options
	ctx     context.Context

	updateNow chan updateRequest

	mu        sync.Mutex
	interval  time.Duration
	runCancel context.CancelFunc // the sync in progress, so Pause can stop it
	update    updateStatus
}

func runTray() {
	e, done, err := open()
	// Just updated: the previous version may still be closing the state file.
	for i := 0; err != nil && os.Getenv(restartEnv) != "" && i < 15; i++ {
		time.Sleep(2 * time.Second)
		e, done, err = open()
	}
	if err != nil {
		// Already running: bring that one's window forward instead of starting another.
		signalShow()
		return
	}
	defer done()

	// First run from a download: install for this user (no admin rights), then run from there.
	if !install.IsInstalled() && os.Getenv("BINALY_SYNC_NO_INSTALL") == "" {
		if target, err := install.Install(); err == nil {
			_ = install.SetStartAtLogon(e.Cfg.Settings().StartAtLogon, target)
			done()
			if err := exec.Command(target).Start(); err == nil {
				return
			}
		}
	} else {
		_ = install.SetStartAtLogon(e.Cfg.Settings().StartAtLogon, install.Target())
	}
	if install.IsInstalled() {
		install.Cleanup()
		_ = install.Register(Version)
		// Right after an update the previous version may still be closing, holding its file;
		// try again once it has surely gone.
		go func() {
			time.Sleep(30 * time.Second)
			install.Cleanup()
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &trayApp{e: e, trigger: make(chan engine.Options, 4), updateNow: make(chan updateRequest, 1), ctx: ctx, interval: 15 * time.Minute}
	app.win = &ui.Window{B: app, DataPath: filepath.Join(config.Dir(), "webview"), OnError: app.windowError}
	_ = os.Remove(filepath.Join(config.Dir(), "ui.url")) // from versions that served the screens on a local port
	systray.Run(app.onReady, func() { cancel() })
}

func (a *trayApp) onReady() {
	normal, _, _ := trayIcons()
	systray.SetIcon(normal)
	systray.SetTitle("Binaly Sync")
	systray.SetTooltip("Binaly Sync")

	// The tray is for opening the window and keeping the program current; everything else
	// lives in the window, so there's one place for each thing. A left click opens the window,
	// a right click shows this menu.
	mUpdate := systray.AddMenuItem("Install update", "A newer version of Binaly Sync is available")
	mUpdate.Hide()
	mStatus := systray.AddMenuItem("Open Binaly Sync", "Progress, folders and settings")
	systray.AddSeparator()
	mVersion := systray.AddMenuItem("Binaly Sync "+versionLabel(), "")
	mVersion.Disable()
	mCheck := systray.AddMenuItem("Check for updates", "")
	mQuit := systray.AddMenuItem("Quit", "")
	systray.SetOnTapped(func() { a.win.Show("status") })

	go a.scheduler()
	go a.refreshTray()
	go a.updater(mUpdate)
	go listenShow(a.ctx, func() { a.win.Show("status") })

	// First run: sign in, then choose a folder and preview it.
	if len(a.e.Cfg.Accounts()) == 0 {
		go func() {
			if err := a.SignIn(); err == nil {
				a.addFolderFlow()
			} else {
				a.win.Show("status")
			}
		}()
	}

	go func() {
		for {
			select {
			case <-mUpdate.ClickedCh:
				mUpdate.Hide()
				a.InstallUpdate()
			case <-mStatus.ClickedCh:
				a.win.Show("status")
			case <-mCheck.ClickedCh:
				a.win.Show("about") // the check's progress shows there
				a.CheckUpdate()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// scheduler runs a sync at start, every interval, and on demand.
func (a *trayApp) scheduler() {
	run := func(opt engine.Options) {
		ctx, cancel := context.WithCancel(a.ctx)
		a.mu.Lock()
		a.runCancel = cancel
		a.mu.Unlock()
		_ = a.e.Run(ctx, opt)
		a.mu.Lock()
		a.runCancel = nil
		a.mu.Unlock()
		cancel()
		if s := a.e.Snapshot(); len(s.Workspaces) > 0 {
			a.mu.Lock()
			a.interval = 15 * time.Minute
			a.mu.Unlock()
		}
	}
	run(engine.Options{})
	for {
		a.mu.Lock()
		wait := a.interval
		a.mu.Unlock()
		select {
		case <-a.ctx.Done():
			return
		case opt := <-a.trigger:
			run(opt)
		case <-time.After(wait):
			run(engine.Options{})
		}
	}
}

// refreshTray keeps the icon and tooltip in step with the engine.
func (a *trayApp) refreshTray() {
	var lastIcon []byte
	for {
		s := a.e.Snapshot()
		iconNormal, iconAttention, iconPaused := trayIcons()
		icon, tip := iconNormal, "Binaly Sync: up to date"
		var p api.Progress
		attention := false
		for _, w := range s.Workspaces {
			if w.State != "ok" || len(w.Warnings) > 0 {
				attention = true
			}
			if w.Progress != nil {
				p.Total += w.Progress.Total
				p.Synced += w.Progress.Synced
				p.Processed += w.Progress.Processed
			}
		}
		if p.Total > 0 {
			tip = fmt.Sprintf("Binaly Sync: %d of %d synced, %d processed", p.Synced, p.Total, p.Processed)
		}
		switch {
		case a.pausedUntil() != "":
			icon, tip = iconPaused, "Binaly Sync: paused"
		case attention:
			icon = iconAttention
			tip += " - needs attention"
		case s.Running:
			tip = "Binaly Sync: " + s.Activity
		}
		if len(icon) != len(lastIcon) || &icon[0] != &lastIcon[0] {
			systray.SetIcon(icon)
			lastIcon = icon
		}
		if len(tip) > 120 {
			tip = tip[:120]
		}
		systray.SetTooltip(tip)
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// pausedUntil is what the window shows: "" when running, config.PausedIndefinitely, or the
// RFC 3339 time the pause ends.
func (a *trayApp) pausedUntil() string {
	paused, until := a.e.Cfg.Settings().Paused(time.Now())
	switch {
	case !paused:
		return ""
	case until.IsZero():
		return config.PausedIndefinitely
	default:
		return until.Format(time.RFC3339)
	}
}

func (a *trayApp) addFolderFlow() {
	folder, err := a.PickFolder()
	if err != nil || folder == "" {
		return
	}
	a.win.Show("preview?folder=" + urlEscape(folder))
}

func urlEscape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", "#", "%23", "&", "%26", "?", "%3F", "+", "%2B")
	return r.Replace(s)
}

// windowError explains a window that can't open, and offers the fix.
func (a *trayApp) windowError(err error) {
	if errors.Is(err, ui.ErrNoWebView) {
		if dialog.Message("%s.\n\nOpen Microsoft's download page for it?", err.Error()).Title("Binaly Sync").YesNo() {
			_ = openBrowser("https://developer.microsoft.com/microsoft-edge/webview2/")
		}
		return
	}
	dialog.Message("%s", err.Error()).Title("Binaly Sync").Error()
}

// openInBinaly opens one lab in the web app. The lab is named in the address, so someone in
// two labs lands in the right one, even when they first have to sign in to the app.
func (a *trayApp) openInBinaly(acct config.Account) {
	_ = openBrowser(appURL(acct.Server) + "/?group_id=" + url.QueryEscape(acct.WorkspaceID))
}

// OpenInBinaly opens one lab in the web app (each lab's card in the window has the button).
func (a *trayApp) OpenInBinaly(accountKey string) {
	for _, acct := range a.e.Cfg.Accounts() {
		if acct.Key() == accountKey {
			a.openInBinaly(acct)
			return
		}
	}
}

// OpenURL opens one of the About page's fixed links (ui.Links) in the browser.
func (a *trayApp) OpenURL(u string) { _ = openBrowser(u) }

// OpenSetup opens a lab's Binaly Sync page in the app at "Set up processing": the steps for the
// scheduled task in Claude, kept in one place (the server) so the window can't drift from them.
func (a *trayApp) OpenSetup(accountKey string) {
	for _, acct := range a.e.Cfg.Accounts() {
		if acct.Key() == accountKey {
			_ = openBrowser(appURL(acct.Server) + "/sync?group_id=" + url.QueryEscape(acct.WorkspaceID) + "#processing")
			return
		}
	}
}

// appURL is the web app for a server: mcp.binaly.ai -> app.binaly.ai.
func appURL(server string) string {
	if strings.Contains(server, "://mcp.") {
		return strings.Replace(server, "://mcp.", "://app.", 1)
	}
	return server
}

// ---- ui.Backend ----

type accountView struct {
	Key           string `json:"key"`
	WorkspaceName string `json:"workspaceName"`
	Email         string `json:"email,omitempty"`
}

func (a *trayApp) Status() any {
	var accts []accountView
	for _, x := range a.e.Cfg.Accounts() {
		accts = append(accts, accountView{Key: x.Key(), WorkspaceName: x.WorkspaceName, Email: x.Email})
	}
	a.mu.Lock()
	upd := a.update
	a.mu.Unlock()
	return map[string]any{"status": a.e.Snapshot(), "accounts": accts, "pausedUntil": a.pausedUntil(), "version": Version, "update": upd}
}

// previewView adds what the page needs beyond the counts: the limit, and whether the folder is
// already synced as part of another one (per workspace, since each lab syncs its own folders).
type previewView struct {
	PreviewResult
	UploadLimit int64
	// Folders already synced that contain this one, by workspace (account key).
	InsideOf []containingFolder
}

type containingFolder struct {
	AccountKey string
	Workspace  string
	Folder     string
}

// containing lists the synced folders that already include path, from the latest status.
func (a *trayApp) containing(path string) []containingFolder {
	var out []containingFolder
	for _, w := range a.e.Snapshot().Workspaces {
		for _, f := range w.Folders {
			if pathx.IsUnder(f.Path, path) {
				out = append(out, containingFolder{AccountKey: w.Key, Workspace: w.Name, Folder: f.Path})
			}
		}
	}
	return out
}

func (a *trayApp) Preview(folder string) (any, error) {
	if folder == "" {
		return nil, errors.New("no folder chosen")
	}
	p := pathx.Canonical(folder)
	if svc := scan.CloudDrive(p); svc != "" {
		return nil, fmt.Errorf("%s is on %s. Its files download in full whenever anything reads them, so syncing it would download the whole drive. Binaly Sync doesn't sync cloud drives yet", folder, svc)
	}
	r, err := Preview(folder, api.DefaultPolicy)
	if err != nil {
		return nil, err
	}
	return previewView{r, api.DefaultPolicy.UploadLimitBytes, a.containing(p)}, nil
}

// RemoveFolder stops this computer syncing one folder for one workspace. Files already in
// Binaly, and notes made from them, stay; another computer syncing the same folder carries on.
func (a *trayApp) RemoveFolder(accountKey, folderID string) error {
	for _, acct := range a.e.Cfg.Accounts() {
		if acct.Key() != accountKey {
			continue
		}
		h, err := a.e.Hello(a.ctx, acct)
		if err != nil {
			return err
		}
		if err := a.e.Client(a.ctx, acct).LeaveFolder(a.ctx, h.MachineID, folderID); err != nil {
			return err
		}
		_ = a.e.State.DropFolder(acct.Key(), folderID)
		a.e.ForgetFolder(acct.Key(), folderID) // gone from the window now, not at the next refresh
		a.SyncNow()                            // refreshes the bar
		return nil
	}
	return errors.New("that workspace isn't signed in on this computer")
}

func (a *trayApp) Start(folder, accountKey string) error {
	var acct *config.Account
	accts := a.e.Cfg.Accounts()
	for i := range accts {
		if accts[i].Key() == accountKey || (accountKey == "" && len(accts) == 1) {
			acct = &accts[i]
		}
	}
	if acct == nil {
		return errors.New("choose a workspace first")
	}
	p := pathx.Canonical(folder)
	h, err := a.e.Hello(a.ctx, *acct)
	if err != nil {
		return err
	}
	// Already covered by a folder this computer syncs for this lab: adding it would only walk
	// the same files twice. (A folder that CONTAINS a synced one is fine: it covers more.)
	for _, f := range h.Folders {
		if pathx.IsUnder(f.Path, p) {
			return fmt.Errorf("%s is already synced, as part of %s. Adding it separately changes nothing", p, f.Path)
		}
	}
	if _, _, err := a.e.Client(a.ctx, *acct).AddFolder(a.ctx, h.MachineID, p); err != nil {
		return err
	}
	a.SyncNow()
	return nil
}

func (a *trayApp) SyncNow() {
	select {
	case a.trigger <- engine.Options{}:
	default:
	}
}

func (a *trayApp) ConfirmDeletions(accountKey string) {
	ws := ""
	for _, x := range a.e.Cfg.Accounts() {
		if x.Key() == accountKey {
			ws = x.WorkspaceID
		}
	}
	select {
	case a.trigger <- engine.Options{Workspace: ws, Force: true, ConfirmMassDeletion: true}:
	default:
	}
}

// Pause stops syncing for d, or until the person resumes when d is 0. A sync in progress
// stops now: what it hadn't uploaded is sent at the next run, and a scan cut short reports
// no deletions.
func (a *trayApp) Pause(d time.Duration) {
	until := config.PausedIndefinitely
	if d > 0 {
		until = time.Now().Add(d).Format(time.RFC3339)
	}
	_ = a.e.Cfg.UpdateSettings(func(s *config.Settings) { s.PausedUntil = until })
	a.mu.Lock()
	cancel := a.runCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *trayApp) Resume() {
	_ = a.e.Cfg.UpdateSettings(func(s *config.Settings) { s.PausedUntil = "" })
	a.SyncNow()
}

func (a *trayApp) Settings() any { return a.e.Cfg.Settings() }

func (a *trayApp) SaveSettings(raw json.RawMessage) error {
	var in struct {
		StartAtLogon  bool   `json:"startAtLogon"`
		Updates       string `json:"updates"`
		BandwidthKBps int    `json:"bandwidthKBps"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	if in.Updates != "auto" && in.Updates != "notify" {
		in.Updates = "auto"
	}
	if in.BandwidthKBps < 0 {
		in.BandwidthKBps = 0
	}
	if err := a.e.Cfg.UpdateSettings(func(s *config.Settings) {
		s.StartAtLogon, s.Updates, s.BandwidthKBps = in.StartAtLogon, in.Updates, in.BandwidthKBps
	}); err != nil {
		return err
	}
	return install.SetStartAtLogon(in.StartAtLogon, install.Target())
}

func (a *trayApp) SignIn() error {
	_, err := Login(a.ctx, a.e, defaultServer(), "")
	if err == nil {
		a.SyncNow()
	}
	return err
}

func (a *trayApp) SignOut(accountKey string) error {
	_ = a.e.Creds.Delete(accountKey)
	return a.e.Cfg.RemoveAccount(accountKey)
}

func (a *trayApp) PickFolder() (string, error) {
	return pickFolder("Choose a folder to sync with Binaly")
}

func (a *trayApp) OpenLog() {
	_ = exec.Command("explorer", runlog.Dir(config.Dir())).Start()
}
