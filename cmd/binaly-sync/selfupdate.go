package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
	"github.com/sqweek/dialog"

	"github.com/binaly-ai/sync/internal/install"
	"github.com/binaly-ai/sync/internal/update"
)

// restartEnv marks a start that follows an update: the old process may still be letting go of
// the state file, so the new one waits for it instead of giving up.
const restartEnv = "BINALY_SYNC_RESTART"

func checker() *update.Checker {
	c := update.New()
	// For testing an update end to end against a local copy of a release.
	if b := os.Getenv("BINALY_SYNC_UPDATE_BASE"); b != "" {
		c.Base = b
	}
	return c
}

// updateStatus is what the status page shows about updates.
type updateStatus struct {
	Available  string `json:"available,omitempty"`
	Installing bool   `json:"installing,omitempty"`
	Error      string `json:"error,omitempty"`
}

// updateRequest is what someone asked for: "Check for updates", or "Install update".
type updateRequest int

const (
	scheduled updateRequest = iota
	checkNow
	installNow
)

// updater checks for a release a couple of minutes after start, then every 6 hours. With
// automatic updates on, it installs one when no sync is running and restarts; otherwise it
// offers it in the tray menu and on the status page. "Check for updates" always answers.
func (a *trayApp) updater(menu *systray.MenuItem) {
	c := checker()
	// A development build, or a copy run from somewhere other than its install folder,
	// doesn't update itself: replacing it would replace the wrong file.
	eligible := Version != "dev" && install.IsInstalled()
	var next <-chan time.Time
	if eligible {
		next = time.After(2 * time.Minute)
	}
	for {
		req := scheduled
		select {
		case <-a.ctx.Done():
			return
		case <-next:
		case req = <-a.updateNow:
		}
		if eligible {
			next = time.After(6 * time.Hour)
		}
		manual := req != scheduled
		if !eligible {
			if manual {
				tell("This copy of Binaly Sync (%s) isn't an installed release, so it doesn't update itself.", versionLabel())
			}
			continue
		}
		r, err := c.Latest(a.ctx, Version, runtime.GOARCH)
		a.setUpdate(func(u *updateStatus) {
			u.Available, u.Error = "", errText(err)
			if r != nil {
				u.Available = r.Version
			}
		})
		switch {
		case err != nil:
			if manual {
				dialog.Message("%s", errText(err)).Title("Binaly Sync").Error()
			}
			continue
		case r == nil:
			if manual {
				tell("Binaly Sync %s is the latest version.", versionLabel())
			}
			continue
		case req == installNow || a.e.Cfg.Settings().Updates == "auto":
		case req == checkNow && dialog.Message("Binaly Sync %s is available (this is %s). Install it now?\n\nIt restarts by itself; syncing carries on.", strings.TrimPrefix(r.Version, "v"), versionLabel()).Title("Binaly Sync").YesNo():
		default:
			menu.SetTitle("Install update " + strings.TrimPrefix(r.Version, "v"))
			menu.Show()
			continue
		}
		if err := a.applyUpdate(c, r); err != nil {
			a.setUpdate(func(u *updateStatus) { u.Installing, u.Error = false, err.Error() })
			if manual {
				dialog.Message("%s", err.Error()).Title("Binaly Sync").Error()
			}
		}
	}
}

// versionLabel is the version as people read it: "0.1.1", not "v0.1.1".
func versionLabel() string { return strings.TrimPrefix(Version, "v") }

func tell(format string, args ...any) {
	dialog.Message(format, args...).Title("Binaly Sync").Info()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return "Couldn't check for updates: " + err.Error()
}

func (a *trayApp) setUpdate(f func(*updateStatus)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(&a.update)
}

// InstallUpdate is the "Install update" button and menu item.
func (a *trayApp) InstallUpdate() { a.requestUpdate(installNow) }

// CheckUpdate is "Check for updates", in the tray menu and on the Settings page.
func (a *trayApp) CheckUpdate() { a.requestUpdate(checkNow) }

func (a *trayApp) requestUpdate(r updateRequest) {
	select {
	case a.updateNow <- r:
	default: // one is already under way
	}
}

// applyUpdate downloads and verifies a release, waits for any running sync to finish, swaps
// the executable and restarts into it. On success it doesn't return: the app quits.
func (a *trayApp) applyUpdate(c *update.Checker, r *update.Release) error {
	a.setUpdate(func(u *updateStatus) { u.Installing = true })
	dl := install.Target() + ".update"
	if err := c.Download(a.ctx, r, dl); err != nil {
		return fmt.Errorf("couldn't download %s: %w", r.Version, err)
	}
	for a.e.Snapshot().Running {
		select {
		case <-a.ctx.Done():
			os.Remove(dl)
			return a.ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if err := restartInto(dl); err != nil {
		return err
	}
	systray.Quit()
	return nil
}

// restartInto swaps in a verified executable and starts it. If it won't start, the previous
// executable goes back.
func restartInto(newExe string) error {
	if err := install.Swap(newExe); err != nil {
		os.Remove(newExe)
		return fmt.Errorf("couldn't replace the program: %w", err)
	}
	cmd := exec.Command(install.Target())
	cmd.Env = append(os.Environ(), restartEnv+"=1")
	if err := cmd.Start(); err != nil {
		_ = os.Rename(install.Target(), newExe)
		_ = os.Rename(install.Target()+".old", install.Target())
		os.Remove(newExe)
		return fmt.Errorf("the new version wouldn't start, so the previous one was kept: %w", err)
	}
	return nil
}

// cmdUpdate installs a newer release now, for IT staff and support. If the tray app is
// running, it's restarted on the new version.
func cmdUpdate(ctx context.Context) error {
	if Version == "dev" {
		return errors.New("this is a development build; it doesn't update itself")
	}
	if _, err := os.Stat(install.Target()); err != nil {
		return errors.New("Binaly Sync isn't installed on this computer; run the downloaded binaly-sync.exe first")
	}
	c := checker()
	r, err := c.Latest(ctx, Version, runtime.GOARCH)
	if err != nil {
		return err
	}
	if r == nil {
		fmt.Println("Binaly Sync", Version, "is the latest version.")
		return nil
	}
	fmt.Printf("Downloading %s...\n", r.Version)
	dl := install.Target() + ".update"
	if err := c.Download(ctx, r, dl); err != nil {
		return err
	}
	stopOthers()
	if err := restartInto(dl); err != nil {
		return err
	}
	fmt.Printf("Updated to %s; Binaly Sync is running in the tray.\n", r.Version)
	return nil
}
