package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/extract"
	"github.com/binaly-ai/sync/internal/onenote"
	"github.com/binaly-ai/sync/internal/pathx"
	"github.com/binaly-ai/sync/internal/runlog"
	"github.com/binaly-ai/sync/internal/scan"
	"github.com/binaly-ai/sync/internal/state"
)

// Decision 24 (docs/sync-plan.md 4.10, server repo): Sync sends what it can read exactly - each
// file's embedded metadata with its report, and, when the server asks, its text page by page -
// so the model reading the lab's files gets whole documents, dates and handwriting flags
// instead of skimming.

// fileMeta returns a file's embedded metadata, read once per version of the file.
func (e *Engine) fileMeta(key string, en scan.Entry, lg *runlog.Log) *api.FileMeta {
	mt := en.ModTime.UnixNano()
	if raw, ok := e.State.CachedMeta(key, en.Size, mt); ok {
		return decodeMeta(raw)
	}
	m, err := extract.ReadMeta(en.Path)
	if err != nil {
		lg.Event("meta-skipped", map[string]any{"path": en.Path, "error": err.Error()})
	}
	var raw json.RawMessage
	if m != nil {
		raw, _ = json.Marshal(m)
	}
	// Recorded even when empty or unreadable, so the file isn't opened again until it changes.
	_ = e.State.PutMeta(key, state.FileMeta{Size: en.Size, MtimeNs: mt, Meta: raw})
	return m
}

// cachedMeta returns metadata recorded earlier (a OneNote section's, at export), never
// reading the file.
func (e *Engine) cachedMeta(key string, en scan.Entry) *api.FileMeta {
	if raw, ok := e.State.CachedMeta(key, en.Size, en.ModTime.UnixNano()); ok {
		return decodeMeta(raw)
	}
	return nil
}

func decodeMeta(raw json.RawMessage) *api.FileMeta {
	if len(raw) == 0 {
		return nil
	}
	var m api.FileMeta
	if json.Unmarshal(raw, &m) != nil || m.Empty() {
		return nil
	}
	return &m
}

// sendExtract sends one content's text, as the server asked (wantExtract). A file that can't
// be read for text is reported with the reason, so the server stops asking; a file that's
// only unreachable for now (locked, changing, its drive gone) is left for a later run.
func (e *Engine) sendExtract(ctx context.Context, j job, lg *runlog.Log) {
	in := api.ExtractIn{MachineID: j.t.acct.MachineID, SHA256: j.sha, Path: pathx.Canonical(j.entry.Path)}
	if j.pdf != nil {
		in.Kind = "onenote"
		se, err := e.loadSectionExtract(ctx, j)
		if err != nil {
			lg.Event("extract-skipped", map[string]any{"path": j.entry.Path, "error": err.Error()})
			return
		}
		in.Pages = se.Pages
		d := &extract.Doc{Kind: "onenote", Pages: in.Pages}
		extract.Finish(d)
		in.Pages, in.Truncated = d.Pages, d.Truncated
	} else {
		in.Kind = extract.Kind(j.entry.Path)
		if in.Kind == "" {
			return // a format Sync doesn't read; the server shouldn't have asked
		}
		e.setActivity("Reading the text of %s", filepath.Base(j.entry.Path))
		d, err := extract.File(ctx, j.entry.Path, j.entry.Size)
		switch {
		case err == nil:
			in.Pages, in.Truncated = d.Pages, d.Truncated
		case transient(err) || ctx.Err() != nil:
			lg.Event("extract-skipped", map[string]any{"path": j.entry.Path, "error": err.Error()})
			return
		default:
			in.Pages, in.Error = []api.ExtractPage{}, err.Error()
		}
	}
	if err := j.t.client.Extract(ctx, in); err != nil {
		lg.Event("extract-failed", map[string]any{"path": j.entry.Path, "error": err.Error()})
		return
	}
	_ = e.State.SetExtractSent(j.t.acct.Key(), j.sha, time.Now())
	if j.pdf != nil {
		os.Remove(e.extractFile(j.sha))
	}
	lg.Event("extract-sent", map[string]any{"path": j.entry.Path, "kind": in.Kind, "pages": len(in.Pages), "truncated": in.Truncated, "error": in.Error})
}

// transient: the file couldn't be opened right now, which a later run may not hit.
func transient(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) || errors.Is(err, scan.ErrChanging)
}

// loadSectionExtract returns a section's pages as read at export. A section exported by an
// earlier version of Sync has none on file: its pages are read again now, with a throwaway
// PDF, so the PDF the server already holds keeps its hash and isn't taken for a new version.
func (e *Engine) loadSectionExtract(ctx context.Context, j job) (*sectionExtract, error) {
	if raw, err := os.ReadFile(e.extractFile(j.sha)); err == nil {
		var se sectionExtract
		if json.Unmarshal(raw, &se) == nil {
			return &se, nil
		}
	}
	pr, ok := e.OneNote.(PageReader)
	if !ok {
		return nil, errors.New("this OneNote export can't read pages")
	}
	fi, err := os.Stat(j.entry.Path)
	if err != nil {
		return nil, err
	}
	if fi.Size() != j.entry.Size || !fi.ModTime().Equal(j.entry.ModTime) {
		return nil, scan.ErrChanging
	}
	if err := os.MkdirAll(e.Renditions, 0o700); err != nil {
		return nil, err
	}
	tmp := filepath.Join(e.Renditions, "pages-"+time.Now().Format("150405.000000000")+".pdf")
	defer os.Remove(tmp)
	e.setActivity("Reading the pages of OneNote section %s", filepath.Base(j.entry.Path))
	raw, err := pr.ExportPages(ctx, j.entry.Path, tmp)
	if err != nil {
		return nil, err
	}
	return sectionFromPages(j.entry.Path, raw), nil
}

// sectionFromPages turns a section's pages, as OneNote gave them, into its extract and its
// metadata: the section's name, page count, first page created, last page modified, and the
// people who wrote most of it.
func sectionFromPages(section string, raw []onenote.RawPage) *sectionExtract {
	se := &sectionExtract{Pages: make([]api.ExtractPage, 0, len(raw))}
	m := &api.FileMeta{Title: strings.TrimSuffix(filepath.Base(section), filepath.Ext(section)), Pages: len(raw)}
	authors := map[string]int{}
	for _, r := range raw {
		p := onenote.ParsePage(r)
		author := ""
		if len(p.Authors) > 0 {
			top := p.Authors
			if len(top) > 3 {
				top = top[:3]
			}
			author = strings.Join(top, ", ")
			authors[p.Authors[0]]++
		}
		se.Pages = append(se.Pages, api.ExtractPage{
			Text: p.Text, Images: p.Images, Hash: p.Hash,
			Title: p.Title, Created: p.Created, Modified: p.Modified, Author: author,
		})
		if p.Created != "" && (m.Created == "" || p.Created < m.Created) {
			m.Created = p.Created
		}
		if p.Modified != "" && p.Modified > m.Modified {
			m.Modified = p.Modified
		}
	}
	names := make([]string, 0, len(authors))
	for n := range authors {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if authors[names[i]] != authors[names[j]] {
			return authors[names[i]] > authors[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > 3 {
		names = names[:3]
	}
	m.Author = strings.Join(names, ", ")
	se.Meta = m
	return se
}
