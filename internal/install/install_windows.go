//go:build windows

// Package install puts Binaly Sync where it belongs for the current user - no administrator
// rights, nothing machine-wide - registers it with Windows (Installed apps, the Start menu,
// start at logon), swaps in updates, and removes all of it again.
package install

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\BinalySync`
	valueName    = "Binaly Sync"
	publisher    = "Binaly Management Ltd"
)

// Dir is %LOCALAPPDATA%\Programs\Binaly.
func Dir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Binaly")
}

// Target is the installed executable's path.
func Target() string { return filepath.Join(Dir(), "binaly-sync.exe") }

// shortcut is the Start menu entry.
func shortcut() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`, "Binaly Sync.lnk")
}

// IsInstalled reports whether the running executable is the installed copy.
func IsInstalled() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(self), filepath.Clean(Target()))
}

// Install copies the running executable into place and returns the installed path.
func Install() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := Target() + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	out.Close()
	return Target(), Swap(tmp)
}

// Swap replaces the installed executable with src (a verified update, or a fresh copy).
// Windows won't overwrite a running exe but will rename it, so the old one moves aside to
// .old, and the next start removes it (Cleanup).
func Swap(src string) error {
	if _, err := os.Stat(Target()); err == nil {
		_ = os.Remove(Target() + ".old")
		if err := os.Rename(Target(), Target()+".old"); err != nil {
			return err
		}
	}
	if err := os.Rename(src, Target()); err != nil {
		_ = os.Rename(Target()+".old", Target()) // put the old one back
		return err
	}
	return nil
}

// Cleanup removes what an earlier update or install left behind. The previous exe can only be
// deleted once it has stopped running.
func Cleanup() {
	for _, ext := range []string{".old", ".new", ".update", ".update.part"} {
		_ = os.Remove(Target() + ext)
	}
}

// Register lists Binaly Sync in Windows' Installed apps (so it can be removed the usual way)
// and adds the Start menu shortcut. It runs at every start, so the listed version follows
// updates.
func Register(version string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	exe := Target()
	var size uint32
	if fi, err := os.Stat(exe); err == nil {
		size = uint32(fi.Size() / 1024)
	}
	strs := map[string]string{
		"DisplayName":          valueName,
		"DisplayVersion":       strings.TrimPrefix(version, "v"),
		"Publisher":            publisher,
		"DisplayIcon":          exe + ",0",
		"InstallLocation":      Dir(),
		"UninstallString":      `"` + exe + `" uninstall`,
		"QuietUninstallString": `"` + exe + `" uninstall --quiet`,
		"URLInfoAbout":         "https://binaly.ai",
		"HelpLink":             "https://github.com/binaly-ai/sync",
	}
	for name, v := range strs {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	for name, v := range map[string]uint32{"NoModify": 1, "NoRepair": 1, "EstimatedSize": size} {
		if err := k.SetDWordValue(name, v); err != nil {
			return err
		}
	}
	return makeShortcut(shortcut(), exe)
}

// makeShortcut writes a .lnk through the Windows Script Host's COM object, the standard way
// without cgo.
func makeShortcut(lnk, target string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		// S_FALSE: COM was already initialized on this thread, which is fine.
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return err
		}
	}
	defer ole.CoUninitialize()
	obj, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return err
	}
	defer obj.Release()
	shell, err := obj.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer shell.Release()
	v, err := oleutil.CallMethod(shell, "CreateShortcut", lnk)
	if err != nil {
		return err
	}
	sc := v.ToIDispatch()
	defer sc.Release()
	for prop, val := range map[string]string{
		"TargetPath":       target,
		"WorkingDirectory": filepath.Dir(target),
		"IconLocation":     target + ",0",
		"Description":      "Keeps your lab's folders connected to Binaly",
	} {
		if _, err := oleutil.PutProperty(sc, prop, val); err != nil {
			return err
		}
	}
	_, err = oleutil.CallMethod(sc, "Save")
	return err
}

// Unregister removes the Installed apps entry, the Start menu shortcut and start at logon.
// The files themselves are removed by the caller, which may be the exe being deleted.
func Unregister() error {
	var errs []error
	if err := registry.DeleteKey(registry.CURRENT_USER, uninstallKey); err != nil && !errors.Is(err, registry.ErrNotExist) {
		errs = append(errs, err)
	}
	if err := SetStartAtLogon(false, ""); err != nil {
		errs = append(errs, err)
	}
	if err := os.Remove(shortcut()); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SetStartAtLogon adds or removes the per-user Run entry.
func SetStartAtLogon(on bool, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(valueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	return k.SetStringValue(valueName, `"`+exe+`"`)
}
