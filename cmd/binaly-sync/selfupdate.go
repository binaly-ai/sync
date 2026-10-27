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

// updateStatus is what the window shows about updates, as it happens: no dialog boxes.
type updateStatus struct {
	// Phase: "" (idle), "checking", "downloading" or "installing".
	Phase string `json:"phase,omitempty"`
	// Available is a newer version found and not yet installed.
	Available string `json:"available,omitempty"`
	// Latest: the last check found nothing newer; CheckedAt is when.
	Latest    bool   `json:"latest,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
	Error     string `json:"error,omitempty"`
	// Installing is kept for the status page's notice (Phase "downloading" or "installing").
	Installing bool `json:"installing,omitempty"`
}

// updateRequest is what someone asked for: "Check for updates", or "Install update".
type updateRequest int

const (
	scheduled updateRequest = iota
	checkNow
	installNow
)

// updateEvery is how often a running copy looks for a new release. Every 2 hours, not 6, so a
// fix reaches pilot labs the same working day it ships (2026-10-27).
const updateEvery = 2 * time.Hour

// updater checks for a release a couple of minutes after start, then every updateEvery. With
// automatic updates on, it installs one and restarts; otherwise it offers it in the window and
// the tray menu. Every step shows in the window (About, and the status page) as it happens.
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
			next = time.After(updateEvery)
		}
		if !eligible {
			if req != scheduled {
				a.setUpdate(func(u *updateStatus) {
					*u = updateStatus{Error: fmt.Sprintf("This copy of Binaly Sync (%s) isn't an installed release, so it doesn't update itself.", versionLabel())}
				})
			}
			continue
		}
		a.setUpdate(func(u *updateStatus) { u.Phase, u.Error, u.Latest = "checking", "", false })
		r, err := c.Latest(a.ctx, Version, runtime.GOARCH)
		now := time.Now().Format(time.RFC3339)
		a.setUpdate(func(u *updateStatus) {
			u.Phase, u.Available, u.Error, u.CheckedAt = "", "", errText(err), now
			u.Latest = err == nil && r == nil
			if r != nil {
				u.Available = r.Version
			}
		})
		if err != nil || r == nil {
			continue
		}
		if req != installNow && a.e.Cfg.Settings().Updates != "auto" {
			menu.SetTitle("Install update " + strings.TrimPrefix(r.Version, "v"))
			menu.Show()
			continue
		}
		if err := a.applyUpdate(c, r); err != nil {
			a.setUpdate(func(u *updateStatus) { u.Phase, u.Installing, u.Error = "", false, err.Error() })
		}
	}
}

// versionLabel is the version as people read it: "0.1.1", not "v0.1.1".
func versionLabel() string { return strings.TrimPrefix(Version, "v") }

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

// CheckUpdate is "Check for updates" (About, and the tray menu, which opens About first).
func (a *trayApp) CheckUpdate() { a.requestUpdate(checkNow) }

func (a *trayApp) requestUpdate(r updateRequest) {
	select {
	case a.updateNow <- r:
	default: // one is already under way, and the window shows its progress
	}
}

// applyUpdate downloads and verifies a release, stops any sync in progress (as Pause does:
// what wasn't uploaded is sent after the restart, and a cut-short scan reports no deletions),
// swaps the executable and restarts into it. It never waits for a sync to finish: a first sync
// can run for hours. On success it doesn't return: the app quits.
func (a *trayApp) applyUpdate(c *update.Checker, r *update.Release) error {
	a.setUpdate(func(u *updateStatus) { u.Phase, u.Installing = "downloading", true })
	dl := install.Target() + ".update"
	if err := c.Download(a.ctx, r, dl); err != nil {
		return fmt.Errorf("couldn't download %s: %w", r.Version, err)
	}
	a.setUpdate(func(u *updateStatus) { u.Phase = "installing" })
	a.mu.Lock()
	cancel := a.runCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Give the cancelled sync a moment to save its place.
	for i := 0; i < 60 && a.e.Snapshot().Running; i++ {
		select {
		case <-a.ctx.Done():
			os.Remove(dl)
			return a.ctx.Err()
		case <-time.After(500 * time.Millisecond):
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
