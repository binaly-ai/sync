// Package watch tells the tray app when something changes inside a synced folder, so a new
// result syncs within a minute instead of at the next full look through the folders. It only
// says which folder changed, never what: the walk that follows works out the rest, so a missed
// or overflowed notification costs nothing that the regular walk doesn't catch.
package watch

import "sync"

// Folder is one folder to watch.
type Folder struct {
	Key  string // how the caller names it (pathx.Key of the path)
	Path string
	// Ignore reports whether a change at a path relative to Path should be ignored (a walk
	// would skip that file anyway). Nil ignores nothing.
	Ignore func(rel string) bool
}

// Watcher watches a set of folders.
type Watcher struct {
	mu     sync.Mutex
	active map[string]*watching
	dirty  map[string]bool
	signal chan struct{}
}

type watching struct{ stop func() }

// New returns a watcher with nothing watched yet.
func New() *Watcher {
	return &Watcher{active: map[string]*watching{}, dirty: map[string]bool{}, signal: make(chan struct{}, 1)}
}

// Changed receives a value after a watched folder changes; Take says which folders did.
func (w *Watcher) Changed() <-chan struct{} { return w.signal }

// Take returns the folders that changed since the last Take, and forgets them.
func (w *Watcher) Take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.dirty))
	for k := range w.dirty {
		out = append(out, k)
	}
	w.dirty = map[string]bool{}
	return out
}

// Mark records folders as changed (Take's results, handed back when they couldn't be used).
func (w *Watcher) Mark(keys ...string) {
	if len(keys) == 0 {
		return
	}
	w.mu.Lock()
	for _, k := range keys {
		w.dirty[k] = true
	}
	w.mu.Unlock()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

// Set watches exactly these folders: new ones start, missing ones stop. A folder that can't
// be watched now (a disconnected drive, or a share that doesn't send notifications) is tried
// again at the next Set, and is covered by the regular walk meanwhile.
func (w *Watcher) Set(folders []Folder) {
	want := map[string]Folder{}
	for _, f := range folders {
		want[f.Key] = f
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, a := range w.active {
		if _, ok := want[k]; !ok {
			a.stop()
			delete(w.active, k)
		}
	}
	for k, f := range want {
		if _, ok := w.active[k]; ok {
			continue
		}
		k, a := k, &watching{}
		stop, err := start(f, func() { w.Mark(k) }, func() { w.drop(k, a) })
		if err != nil {
			continue
		}
		a.stop = stop
		w.active[k] = a
	}
}

// Watching returns the folders being watched right now.
func (w *Watcher) Watching() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.active))
	for k := range w.active {
		out = append(out, k)
	}
	return out
}

// Close stops watching everything.
func (w *Watcher) Close() { w.Set(nil) }

// drop forgets a folder whose watching broke (its drive went away), so the next Set restarts
// it. Only that one: the folder may have been stopped and started again since.
func (w *Watcher) drop(k string, a *watching) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active[k] == a {
		delete(w.active, k)
	}
}
