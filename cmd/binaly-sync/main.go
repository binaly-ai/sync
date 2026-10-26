// Binaly Sync keeps a lab's folders connected to Binaly. Run with no arguments it is the tray
// app; with a command it does that one thing and exits (for IT staff, support and scripts).
package main

// The program's icon and version information, from winres/winres.json. The release workflow
// passes the version; a plain go build without this step just has no icon.
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64,arm64

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/auth"
	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/creds"
	"github.com/binaly-ai/sync/internal/engine"
	"github.com/binaly-ai/sync/internal/onenote"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/runlog"
	"github.com/binaly-ai/sync/internal/scan"
	"github.com/binaly-ai/sync/internal/state"
)

// Version is set at release time (-ldflags "-X main.Version=v0.1.0").
var Version = "dev"

const usage = `Binaly Sync %s - keeps your lab's folders connected to Binaly.

Run with no command to start the tray app. Commands:

  login [--server URL] [--token T]   sign in to a workspace (opens your browser)
  logout [--workspace W]             sign out of a workspace
  workspaces                         list signed-in workspaces
  add <folder> [--workspace W]       sync a folder from this computer
  remove <folder> [--workspace W]    stop syncing a folder from this computer
  preview <folder>                   what would be synced - no sign-in, nothing leaves the computer
  sync [--workspace W] [--force]     run one sync now, in the foreground
  status                             progress per workspace and folder
  log                                where the latest run log is
  update                             install a newer release now, if there is one
  uninstall [--quiet]                remove Binaly Sync and its sign-ins from this computer
  version
`

func main() {
	if len(os.Args) < 2 {
		runTray()
		return
	}
	attachConsole()
	cmd, args := os.Args[1], os.Args[2:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch cmd {
	case "login":
		err = cmdLogin(ctx, args)
	case "logout":
		err = cmdLogout(args)
	case "workspaces":
		err = cmdWorkspaces()
	case "add":
		err = cmdAdd(ctx, args)
	case "remove":
		err = cmdRemove(ctx, args)
	case "preview":
		err = cmdPreview(args)
	case "sync":
		err = cmdSync(ctx, args)
	case "status":
		err = cmdStatus(ctx)
	case "log":
		err = cmdLog()
	case "update":
		err = cmdUpdate(ctx)
	case "uninstall":
		err = cmdUninstall(args)
	case "version", "--version", "-v":
		fmt.Println("binaly-sync", Version)
	case "help", "--help", "-h":
		fmt.Printf(usage, Version)
	default:
		fmt.Printf(usage, Version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "binaly-sync:", err)
		os.Exit(1)
	}
}

func defaultServer() string {
	if s := os.Getenv("BINALY_SYNC_SERVER"); s != "" {
		return s
	}
	return config.DefaultServer
}

// open loads settings, credentials and the state file. The state file is the single-instance
// lock: while the tray app runs, a command that needs it waits briefly, then says so.
func open() (*engine.Engine, func(), error) {
	cfg, err := config.Open()
	if err != nil {
		return nil, nil, err
	}
	st, err := state.Open(filepath.Join(config.Dir(), "state.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("another Binaly Sync is running (close it, or use its tray menu): %w", err)
	}
	e := &engine.Engine{Cfg: cfg, State: st, Creds: creds.System(), Version: Version,
		Renditions: filepath.Join(config.Dir(), "onenote", "pdf")}
	// OneNote sections are sent as PDFs its own app exports, where the desktop app is installed.
	var x *onenote.Exporter
	if onenote.Available() {
		x = onenote.New(filepath.Join(config.Dir(), "onenote", "work"))
		e.OneNote = x
	}
	return e, func() {
		if x != nil {
			x.Close()
		}
		st.Close()
	}, nil
}

func openBrowser(url string) error {
	// For automated tests of the sign-in flow: write the address instead of opening a browser.
	if f := os.Getenv("BINALY_SYNC_PRINT_URL"); f != "" {
		return os.WriteFile(f, []byte(url), 0o600)
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// staticTokens wraps a pasted machine token for the first check-in.
type staticTokens string

func (s staticTokens) Token(context.Context) (string, error) { return string(s), nil }
func (staticTokens) Invalidate()                             {}

// Login signs in and records the account. Shared by the command and the tray.
func Login(ctx context.Context, e *engine.Engine, server, token string) (config.Account, error) {
	server = strings.TrimRight(server, "/")
	a := config.Account{Server: server}
	var sec auth.Secret
	var access string
	if token != "" {
		sec = auth.Secret{Static: token}
		access = token
		a.Static = true
	} else {
		meta, err := e.Meta(ctx, server)
		if err != nil {
			return a, fmt.Errorf("can't reach %s: %w", server, err)
		}
		for _, x := range e.Cfg.Accounts() {
			if x.Server == server && x.ClientID != "" {
				a.ClientID = x.ClientID
			}
		}
		if a.ClientID == "" {
			if a.ClientID, err = auth.Register(ctx, meta, "Binaly Sync on "+engine.MachineName()); err != nil {
				return a, err
			}
		}
		fmt.Println("Opening your browser to sign in to Binaly...")
		if sec, err = auth.Login(ctx, meta, a.ClientID, openBrowser); err != nil {
			return a, err
		}
		access = sec.Access
	}
	c := &api.Client{Server: server, Tokens: staticTokens(access)}
	h, err := c.Hello(ctx, api.HelloIn{InstallID: e.Cfg.InstallID(), MachineName: engine.MachineName(), OS: runtime.GOOS, Version: e.Version, DriveMap: pathx.DriveMap()})
	if err != nil {
		return a, fmt.Errorf("signed in, but the first check-in failed: %w", err)
	}
	a.WorkspaceID, a.WorkspaceName, a.MachineID = h.Workspace.ID, h.Workspace.Name, h.MachineID
	if a.WorkspaceID == "" {
		return a, errors.New("the server didn't say which workspace this is; it may need updating")
	}
	if h.User != nil {
		a.UserID, a.Email = h.User.ID, h.User.Email
	}
	if err := auth.NewSource(a.Key(), a.ClientID, "", e.Creds).Save(sec); err != nil {
		return a, fmt.Errorf("couldn't save the sign-in: %w", err)
	}
	return a, e.Cfg.PutAccount(a)
}

func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "Binaly server")
	token := fs.String("token", "", "a machine token instead of a browser sign-in")
	fs.Parse(args)
	e, done, err := open()
	if err != nil {
		return err
	}
	defer done()
	a, err := Login(ctx, e, *server, *token)
	if err != nil {
		return err
	}
	who := a.Email
	if who == "" {
		who = "a machine token"
	}
	fmt.Printf("Signed in to %s as %s.\nNext: binaly-sync add <folder>\n", a.WorkspaceName, who)
	return nil
}

// pick chooses the account a command acts on.
func pick(e *engine.Engine, ws string) (config.Account, error) {
	accts := e.Cfg.Accounts()
	if len(accts) == 0 {
		return config.Account{}, errors.New("not signed in - run: binaly-sync login")
	}
	if ws == "" {
		if len(accts) == 1 {
			return accts[0], nil
		}
		var names []string
		for _, a := range accts {
			names = append(names, a.WorkspaceName)
		}
		return config.Account{}, fmt.Errorf("signed in to several workspaces; add --workspace with one of: %s", strings.Join(names, ", "))
	}
	for _, a := range accts {
		if strings.EqualFold(a.WorkspaceName, ws) || a.WorkspaceID == ws {
			return a, nil
		}
	}
	return config.Account{}, fmt.Errorf("not signed in to a workspace called %q", ws)
}

func splitFolderArgs(name string, args []string) (string, string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	ws := fs.String("workspace", "", "workspace name")
	// Allow the folder before or after the flags.
	var folder string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		folder, args = args[0], args[1:]
	}
	fs.Parse(args)
	if folder == "" && fs.NArg() > 0 {
		folder = fs.Arg(0)
	}
	return folder, *ws
}

func cmdLogout(args []string) error {
	_, ws := splitFolderArgs("logout", args)
	e, done, err := open()
	if err != nil {
		return err
	}
	defer done()
	a, err := pick(e, ws)
	if err != nil {
		return err
	}
	_ = e.Creds.Delete(a.Key())
	if err := e.Cfg.RemoveAccount(a.Key()); err != nil {
		return err
	}
	fmt.Printf("Signed out of %s. Nothing in Binaly was changed.\n", a.WorkspaceName)
	return nil
}

func cmdWorkspaces() error {
	cfg, err := config.Open()
	if err != nil {
		return err
	}
	accts := cfg.Accounts()
	if len(accts) == 0 {
		fmt.Println("Not signed in to any workspace. Run: binaly-sync login")
		return nil
	}
	for _, a := range accts {
		who := a.Email
		if who == "" {
			who = "machine token"
		}
		fmt.Printf("%s  (%s, %s)\n", a.WorkspaceName, who, a.Server)
	}
	return nil
}

func cmdAdd(ctx context.Context, args []string) error {
	folder, ws := splitFolderArgs("add", args)
	if folder == "" {
		return errors.New("which folder? binaly-sync add <folder>")
	}
	e, done, err := open()
	if err != nil {
		return err
	}
	defer done()
	a, err := pick(e, ws)
	if err != nil {
		return err
	}
	p := pathx.Canonical(folder)
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder this computer can read", folder)
	}
	if svc := scan.CloudDrive(p); svc != "" {
		return fmt.Errorf("%s is on %s. Its files download in full whenever anything reads them, so syncing it would download the whole drive. Binaly Sync doesn't sync cloud drives yet", folder, svc)
	}
	h, err := e.Hello(ctx, a)
	if err != nil {
		return err
	}
	f, others, err := e.Client(ctx, a).AddFolder(ctx, h.MachineID, p)
	if err != nil {
		return err
	}
	fmt.Printf("Syncing %s to %s.\n", f.Path, a.WorkspaceName)
	if len(others) > 0 {
		fmt.Printf("Also synced by: %s. This computer helps keep it current.\n", strings.Join(others, ", "))
	}
	fmt.Println("It's picked up at the next sync (binaly-sync sync to run one now).")
	return nil
}

func cmdRemove(ctx context.Context, args []string) error {
	folder, ws := splitFolderArgs("remove", args)
	if folder == "" {
		return errors.New("which folder? binaly-sync remove <folder>")
	}
	e, done, err := open()
	if err != nil {
		return err
	}
	defer done()
	a, err := pick(e, ws)
	if err != nil {
		return err
	}
	h, err := e.Hello(ctx, a)
	if err != nil {
		return err
	}
	key := pathx.Key(pathx.Canonical(folder))
	for _, f := range h.Folders {
		if pathx.Key(f.Path) == key {
			if err := e.Client(ctx, a).LeaveFolder(ctx, h.MachineID, f.ID); err != nil {
				return err
			}
			_ = e.State.DropFolder(a.Key(), f.ID)
			fmt.Printf("This computer no longer syncs %s. Notes made from it stay in Binaly.\n", f.Path)
			return nil
		}
	}
	return fmt.Errorf("this computer doesn't sync %s to %s", folder, a.WorkspaceName)
}

// PreviewResult is what a folder would sync, worked out locally.
type PreviewResult struct {
	Folder       string
	Files        int
	Bytes        int64
	WillUpload   int
	RegisterOnly int
	Excluded     int
	OnlineOnly   int
	Unreadable   int
}

// Preview walks a folder with the given policy. Nothing leaves the computer.
func Preview(folder string, p api.Policy) (PreviewResult, error) {
	r := PreviewResult{Folder: pathx.Canonical(folder)}
	entries, st, err := scan.Walk(r.Folder, scan.NewRules(p, nil))
	if err != nil {
		return r, err
	}
	r.Excluded, r.OnlineOnly, r.Unreadable = st.Excluded, st.OnlineOnly, st.Unreadable
	for _, en := range entries {
		r.Files++
		r.Bytes += en.Size
		if en.Size > p.UploadLimitBytes {
			r.RegisterOnly++
		} else {
			r.WillUpload++
		}
	}
	return r, nil
}

func human(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", b)
}

func cmdPreview(args []string) error {
	if len(args) == 0 {
		return errors.New("which folder? binaly-sync preview <folder>")
	}
	r, err := Preview(args[0], api.DefaultPolicy)
	if err != nil {
		return err
	}
	fmt.Printf("%s   %d files   %s\n", r.Folder, r.Files, human(r.Bytes))
	fmt.Printf("  will upload       %6d   (up to %s each)\n", r.WillUpload, human(api.DefaultPolicy.UploadLimitBytes))
	fmt.Printf("  registered only   %6d   (larger; Claude reads these from the drive)\n", r.RegisterOnly)
	fmt.Printf("  skipped           %6d   (lock files, temp files, system folders)\n", r.Excluded)
	if r.OnlineOnly > 0 {
		fmt.Printf("  online only       %6d   (cloud files not on this disk; not synced)\n", r.OnlineOnly)
	}
	if r.Unreadable > 0 {
		fmt.Printf("  unreadable        %6d   (no permission)\n", r.Unreadable)
	}
	fmt.Println("\nNothing was sent. Binaly makes the final call per file (files over 100 MB stay on")
	fmt.Println("this computer and are recorded by name and fingerprint only).")
	return nil
}

func cmdSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	ws := fs.String("workspace", "", "only this workspace")
	force := fs.Bool("force", false, "walk every folder, even if another computer just did")
	confirm := fs.Bool("confirm-mass-deletion", false, "the files really were removed; let the server mark them gone")
	fs.Parse(args)
	e, done, err := open()
	if err != nil {
		return err
	}
	defer done()
	if len(e.Cfg.Accounts()) == 0 {
		return errors.New("not signed in - run: binaly-sync login")
	}
	if err := e.Run(ctx, engine.Options{Workspace: *ws, Force: *force, ConfirmMassDeletion: *confirm}); err != nil {
		return err
	}
	printStatus(e.Snapshot())
	return nil
}

func printStatus(s engine.Status) {
	for _, w := range s.Workspaces {
		fmt.Printf("%s: %s\n", w.Name, w.State)
		if w.Error != "" {
			fmt.Println("  ", w.Error)
		}
		if w.Progress != nil {
			p := w.Progress
			counting := ""
			if p.Counting {
				counting = " (still counting)"
			}
			fmt.Printf("  %d processed, %d synced, of %d files%s\n", p.Processed, p.Synced, p.Total, counting)
			if p.Attention > 0 {
				fmt.Printf("  %d need attention\n", p.Attention)
			}
			if p.UploadBytesLeft > 0 {
				fmt.Printf("  %s left to upload\n", human(p.UploadBytesLeft))
			}
		}
		folders := append([]api.FolderProgress(nil), w.Folders...)
		sort.Slice(folders, func(i, j int) bool { return folders[i].Path < folders[j].Path })
		for _, f := range folders {
			fmt.Printf("    %s: %d/%d/%d\n", f.Path, f.Processed, f.Synced, f.Total)
		}
		for _, warn := range w.Warnings {
			fmt.Println("  !", warn)
		}
	}
	if s.LastLog != "" {
		fmt.Println("Log:", s.LastLog)
	}
}

func cmdStatus(ctx context.Context) error {
	e, done, err := open()
	if err != nil {
		return err
	}
	defer done()
	var s engine.Status
	for _, a := range e.Cfg.Accounts() {
		w := engine.WorkspaceStatus{Name: a.WorkspaceName, Email: a.Email, Server: a.Server, State: "ok"}
		pr, err := e.Client(ctx, a).GetProgress(ctx)
		if err != nil {
			w.State, w.Error = "error", err.Error()
		} else {
			p := pr.Overall
			w.Progress, w.Folders = &p, pr.Folders
			for _, f := range pr.Folders {
				if f.Warning != nil {
					w.Warnings = append(w.Warnings, f.Warning.Reason)
				}
			}
		}
		s.Workspaces = append(s.Workspaces, w)
	}
	if len(s.Workspaces) == 0 {
		fmt.Println("Not signed in. Run: binaly-sync login")
	}
	printStatus(s)
	return nil
}

func cmdLog() error {
	entries, err := os.ReadDir(runlog.Dir(config.Dir()))
	if err != nil || len(entries) == 0 {
		fmt.Println("No runs yet.")
		return nil
	}
	fmt.Println(filepath.Join(runlog.Dir(config.Dir()), entries[len(entries)-1].Name()))
	return nil
}
