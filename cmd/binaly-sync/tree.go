package main

// The Tree page (the server's docs/sync-plan.md 4.7a): this computer's folders, synced or not,
// with how far each synced folder's files have got, and the lab's marks. The folders come from
// this computer's own disk, so a folder that was never added shows as "not synced"; the states
// come from the server, one level at a time.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/config"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/scan"
)

// treeEntry is one row: a folder or a file, on this computer, in Binaly, or both.
type treeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	// Local: on this computer now. A file only the server knows was removed from the folder.
	Local bool `json:"local"`
	// Synced: inside a folder this computer syncs for the workspace.
	Synced bool `json:"synced"`
	// HoldsSynced: not synced itself, but a synced folder is inside it.
	HoldsSynced bool            `json:"holdsSynced,omitempty"`
	Size        int64           `json:"size,omitempty"`
	Folder      *api.TreeFolder `json:"folder,omitempty"`
	File        *api.TreeFile   `json:"file,omitempty"`
}

type treeView struct {
	Path   string `json:"path"`
	Parent string `json:"parent,omitempty"`
	// SyncedRoot: the synced folder this path is inside, if any.
	SyncedRoot string         `json:"syncedRoot,omitempty"`
	Level      *api.TreeLevel `json:"level,omitempty"`
	// ServerNote: why the server's side is missing (an older server, no connection).
	ServerNote string      `json:"serverNote,omitempty"`
	LocalError string      `json:"localError,omitempty"`
	Entries    []treeEntry `json:"entries"`
	// For the top of the tree: the synced folders, and places to browse from.
	Synced []string `json:"synced,omitempty"`
	Places []string `json:"places,omitempty"`
}

func (a *trayApp) account(key string) (config.Account, error) {
	for _, acct := range a.e.Cfg.Accounts() {
		if acct.Key() == key {
			return acct, nil
		}
	}
	return config.Account{}, errors.New("that workspace isn't signed in on this computer")
}

// places are where browsing for a folder to add can start: the person's own folders and the
// computer's drives.
func places() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, home)
		for _, d := range []string{"Desktop", "Documents"} {
			if st, err := os.Stat(filepath.Join(home, d)); err == nil && st.IsDir() {
				out = append(out, filepath.Join(home, d))
			}
		}
	}
	if runtime.GOOS == "windows" {
		for c := 'C'; c <= 'Z'; c++ {
			root := string(c) + `:\`
			if _, err := os.Stat(root); err == nil {
				out = append(out, root)
			}
		}
	}
	return out
}

// Tree is one level of the tree for one workspace: path "" is the top (synced folders and
// places to browse from).
func (a *trayApp) Tree(accountKey, path string) (any, error) {
	acct, err := a.account(accountKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	h, err := a.e.Hello(ctx, acct)
	if err != nil {
		return nil, err
	}
	if path == "" {
		v := treeView{Places: places(), Entries: []treeEntry{}}
		for _, f := range h.Folders {
			v.Synced = append(v.Synced, f.Path)
		}
		sort.Strings(v.Synced)
		return v, nil
	}

	p := pathx.Canonical(path)
	v := treeView{Path: p, Entries: []treeEntry{}}
	if parent := filepath.Dir(p); parent != p && parent != "." {
		v.Parent = parent
	}
	var root *api.Folder
	for i, f := range h.Folders {
		if pathx.IsUnder(f.Path, p) && (root == nil || len(f.Path) > len(root.Path)) {
			root = &h.Folders[i]
		}
	}
	rules := scan.NewRules(h.Policy, nil)

	byName := map[string]*treeEntry{}
	var order []string
	add := func(e treeEntry) *treeEntry {
		k := strings.ToLower(e.Name)
		if x, ok := byName[k]; ok {
			return x
		}
		e2 := e
		byName[k] = &e2
		order = append(order, k)
		return &e2
	}

	// This computer's side.
	list, err := os.ReadDir(p)
	if err != nil {
		v.LocalError = "This computer can't open this folder now: " + err.Error()
	}
	for _, d := range list {
		name := d.Name()
		if rules.Ignores(name) {
			continue
		}
		child := filepath.Join(p, name)
		e := treeEntry{Name: name, Path: child, Dir: d.IsDir(), Local: true}
		if !e.Dir {
			if info, err := d.Info(); err == nil {
				e.Size = info.Size()
			}
		}
		for _, f := range h.Folders {
			if pathx.IsUnder(f.Path, child) {
				e.Synced = true
			} else if e.Dir && pathx.IsUnder(child, f.Path) {
				e.HoldsSynced = true
			}
		}
		add(e)
	}

	// The server's side, inside a synced folder.
	if root != nil {
		v.SyncedRoot = root.Path
		lvl, err := a.e.Client(ctx, acct).Tree(ctx, h.MachineID, p, 0)
		var ae *api.Error
		switch {
		case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
			v.ServerNote = "Your lab's Binaly doesn't show folder progress yet."
		case err != nil:
			v.ServerNote = "Couldn't get this folder's progress from Binaly: " + err.Error()
		default:
			v.Level = lvl
			for i := range lvl.Folders {
				f := &lvl.Folders[i]
				e := add(treeEntry{Name: f.Name, Path: f.Path, Dir: true, Synced: true})
				e.Folder = f
			}
			for i := range lvl.FilesHere {
				f := &lvl.FilesHere[i]
				e := add(treeEntry{Name: f.Name, Path: f.Path, Synced: true})
				e.File = f
			}
		}
	}

	for _, k := range order {
		v.Entries = append(v.Entries, *byName[k])
	}
	sort.SliceStable(v.Entries, func(i, j int) bool {
		if v.Entries[i].Dir != v.Entries[j].Dir {
			return v.Entries[i].Dir
		}
		return strings.ToLower(v.Entries[i].Name) < strings.ToLower(v.Entries[j].Name)
	})
	return v, nil
}

// Mark sets "first", "leave-out" or "normal" on a folder this computer syncs. A change to what
// is left out starts a sync, so this computer stops (or starts again) sending those files now.
func (a *trayApp) Mark(accountKey, path, mark string) (any, error) {
	acct, err := a.account(accountKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	h, err := a.e.Hello(ctx, acct)
	if err != nil {
		return nil, err
	}
	out, err := a.e.Client(ctx, acct).SetMark(ctx, h.MachineID, pathx.Canonical(path), mark)
	if err != nil {
		return nil, err
	}
	if mark == "leave-out" || mark == "normal" {
		a.SyncNow()
	}
	return map[string]any{"released": out.Released}, nil
}

// Release puts held files at or under a path back in the queue ("Read it").
func (a *trayApp) Release(accountKey, path string) (any, error) {
	acct, err := a.account(accountKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	h, err := a.e.Hello(ctx, acct)
	if err != nil {
		return nil, err
	}
	n, err := a.e.Client(ctx, acct).Release(ctx, h.MachineID, pathx.Canonical(path))
	return map[string]any{"queued": n}, err
}
