package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sqweek/dialog"
	"golang.org/x/sys/windows"

	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/creds"
	"github.com/binaly-ai/sync/internal/install"
)

const removedNote = "Files already in Binaly stay there. To stop this computer appearing in the workspace, an admin can disconnect it in the app."

// cmdUninstall removes Binaly Sync from this computer: the running app, the Installed apps
// entry, the Start menu shortcut, start at logon, the sign-ins in Credential Manager, and the
// program and its local files. Windows' "Uninstall" button runs this.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "don't ask, don't show messages")
	fs.Parse(args)

	if !*quiet {
		ok := dialog.Message("Remove Binaly Sync from this computer?\n\n%s", removedNote).Title("Binaly Sync").YesNo()
		if !ok {
			return nil
		}
	}

	// Stop the tray app (and any other copy), so its files can be deleted.
	stopOthers()

	var errs []error
	if cfg, err := config.Open(); err == nil {
		store := creds.System()
		for _, a := range cfg.Accounts() {
			if err := store.Delete(a.Key()); err != nil {
				errs = append(errs, fmt.Errorf("sign-in for %s: %w", a.WorkspaceName, err))
			}
		}
	}
	if err := install.Unregister(); err != nil {
		errs = append(errs, err)
	}

	// The running exe can't delete itself; a short-lived cmd does it once this process exits.
	var dirs []string
	for _, d := range []string{install.Dir(), config.Dir()} {
		if ours(d) {
			dirs = append(dirs, `rmdir /S /Q "`+d+`"`)
		}
	}
	if parent := filepath.Dir(config.Dir()); ours(config.Dir()) {
		dirs = append(dirs, `rmdir "`+parent+`"`) // %LOCALAPPDATA%\Binaly, only if now empty
	}
	if len(dirs) > 0 {
		c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
		c.SysProcAttr = &syscall.SysProcAttr{
			CmdLine:       `/C ping -n 3 127.0.0.1 >NUL & ` + strings.Join(dirs, " & "),
			HideWindow:    true,
			CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS,
		}
		if err := c.Start(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := errors.Join(errs...); err != nil {
		if !*quiet {
			dialog.Message("Binaly Sync was mostly removed, but not everything:\n\n%v", err).Title("Binaly Sync").Error()
		}
		return err
	}
	fmt.Println("Binaly Sync was removed from this computer. " + removedNote)
	if !*quiet {
		dialog.Message("Binaly Sync was removed from this computer.\n\n%s", removedNote).Title("Binaly Sync").Info()
	}
	return nil
}

// ours is a guard before deleting a folder recursively: only Binaly Sync's own two folders,
// never wherever BINALY_SYNC_HOME or a broken environment points.
func ours(dir string) bool {
	d := strings.ToLower(filepath.Clean(dir))
	local := strings.ToLower(filepath.Clean(os.Getenv("LOCALAPPDATA")))
	if local == "." || local == "" {
		return false
	}
	return d == filepath.Join(local, "programs", "binaly") || d == filepath.Join(local, "binaly", "sync")
}
