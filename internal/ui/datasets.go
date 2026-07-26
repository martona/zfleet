package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// Dataset-cache machinery for the tree: lazy fetches for dataset trees,
// snapshot lists, and properties, plus the snapshot mark set with its
// debounced `zfs destroy -nv` reclaim math. Snapshots surface as tree rows
// (toggled per dataset with `t`), so there is no separate browsing mode —
// one navigation surface, one cursor.

const modePools = 0 // the tree screen; modePerf lives in perf.go

const datasetsInterval = 10 * time.Second

const familyMinSize = 3

// poolOf returns the pool component of a dataset path.
func poolOf(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// dryResult caches one `zfs destroy -nv` output, keyed by its exact target
// string — a changed selection or snapshot set is a different key, so the
// cache self-invalidates.
type dryResult struct {
	text    string
	errText string
	pending bool
}

// The mark set lives on one dataset at a time — reclaim math across
// datasets is meaningless. Marking anywhere else moves the selection there.

// markHostDs resolves the owning (host, dataset) of the current marks.
func (m *Model) markHostDs() (*hostState, string) {
	i := strings.IndexByte(m.markOwner, 0)
	if i < 0 {
		return nil, ""
	}
	return m.hostByName(m.markOwner[:i]), m.markOwner[i+1:]
}

// toggleMark flips selection on a snapshot row or a whole family at once.
func (m *Model) toggleMark(r treeRow) bool {
	if r.kind != rSnap && r.kind != rFam {
		return false
	}
	if m.markOwner != r.parentID {
		m.marks = map[string]bool{}
		m.markOwner = r.parentID
	}
	switch r.kind {
	case rSnap:
		if m.marks[r.snap.Snap] {
			delete(m.marks, r.snap.Snap)
		} else {
			m.marks[r.snap.Snap] = true
		}
	case rFam:
		all := true
		for _, s := range r.fam.Snaps {
			if !m.marks[s.Snap] {
				all = false
				break
			}
		}
		for _, s := range r.fam.Snaps {
			if all {
				delete(m.marks, s.Snap)
			} else {
				m.marks[s.Snap] = true
			}
		}
	}
	m.markGen++
	return true
}

func (m *Model) clearMarks() {
	if len(m.marks) > 0 {
		m.marks = map[string]bool{}
		m.markGen++
	}
	m.markOwner = ""
}

// famAllMarked reports whether every member of a family row is marked.
func (m *Model) famAllMarked(r treeRow) bool {
	if r.parentID != m.markOwner || len(m.marks) == 0 {
		return false
	}
	for _, s := range r.fam.Snaps {
		if !m.marks[s.Snap] {
			return false
		}
	}
	return true
}

// markedSnaps returns the marked snapshots in chronological order,
// intersected with the owner's current snapshot list.
func (m *Model) markedSnaps() []*zfs.Snapshot {
	h, ds := m.markHostDs()
	if h == nil || len(m.marks) == 0 {
		return nil
	}
	var out []*zfs.Snapshot
	for _, s := range h.dsSnaps[ds] {
		if m.marks[s.Snap] {
			out = append(out, s)
		}
	}
	return out
}

// MarkTarget builds the `zfs destroy -n` argument ("ds@a,b,c").
func (m *Model) MarkTarget() string {
	sel := m.markedSnaps()
	if len(sel) == 0 {
		return ""
	}
	_, ds := m.markHostDs()
	names := make([]string, len(sel))
	for i, s := range sel {
		names[i] = s.Snap
	}
	return ds + "@" + strings.Join(names, ",")
}

// famTarget builds the dry-run target for a whole family.
func famTarget(container string, f *zfs.SnapFamily) string {
	if container == "" || len(f.Snaps) == 0 {
		return ""
	}
	names := make([]string, len(f.Snaps))
	for i, s := range f.Snaps {
		names[i] = s.Snap
	}
	return container + "@" + strings.Join(names, ",")
}

// markDebounce schedules the reclaim computation a beat after the last
// mark change, so spacebar streaks cost one exec, not one per press.
func (m *Model) markDebounce() tea.Cmd {
	gen := m.markGen
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return dryTickMsg{gen: gen} })
}

// lazy-fetch helpers

func (m *Model) ensureTreeCmd(h *hostState, pool string) tea.Cmd {
	if h == nil || h.dsTrees[pool] != nil || h.dsTreesPend[pool] {
		return nil
	}
	h.dsTreesPend[pool] = true
	return fetchDatasets(h, pool)
}

func (m *Model) ensureSnapsCmd(h *hostState, name string) tea.Cmd {
	if h == nil {
		return nil
	}
	if _, ok := h.dsSnaps[name]; ok || h.dsSnapsPend[name] {
		return nil
	}
	h.dsSnapsPend[name] = true
	return fetchSnaps(h, name)
}

func (m *Model) ensurePropsCmd(h *hostState, name string) tea.Cmd {
	if h == nil {
		return nil
	}
	if _, ok := h.dsProps[name]; ok || h.dsPropsPend[name] {
		return nil
	}
	h.dsPropsPend[name] = true
	return fetchProps(h, name)
}

// ensureDryCmd runs a dry-run destroy for a target once, caching forever —
// the target string embeds the exact snapshot set, so changes mint new keys.
func (m *Model) ensureDryCmd(h *hostState, target string) tea.Cmd {
	if target == "" || h == nil {
		return nil
	}
	if _, ok := h.dryCache[target]; ok {
		return nil
	}
	h.dryCache[target] = &dryResult{pending: true}
	return fetchDryRun(h, target)
}

// messages and fetch commands

type datasetsMsg struct {
	host string
	pool string
	text string
	err  error
}
type snapsMsg struct {
	host string
	ds   string
	text string
	err  error
}
type propsMsg struct {
	host string
	ds   string
	text string
	err  error
}
type datasetsTickMsg struct{}
type dryTickMsg struct{ gen int }
type dryRunMsg struct {
	host   string
	target string
	text   string
	err    error
}

func fetchDryRun(h *hostState, target string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := src.DestroyDryRun(ctx, target)
		return dryRunMsg{host: host, target: target, text: text, err: err}
	}
}

func fetchDatasets(h *hostState, pool string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.DatasetTexts(ctx, pool)
		return datasetsMsg{host: host, pool: pool, text: text, err: err}
	}
}

func fetchSnaps(h *hostState, ds string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.SnapshotTexts(ctx, ds)
		return snapsMsg{host: host, ds: ds, text: text, err: err}
	}
}

func fetchProps(h *hostState, ds string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.PropTexts(ctx, ds)
		return propsMsg{host: host, ds: ds, text: text, err: err}
	}
}

// exported helpers for --dump

func (m *Model) ApplyDatasets(host, pool, text string) {
	if h := m.hostByName(host); h != nil {
		h.dsTrees[pool] = zfs.ParseDatasets(text)
		delete(h.dsTreesPend, pool)
	}
}

func (m *Model) ApplySnaps(host, ds, text string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	snaps := zfs.ParseSnapshots(text, ds)
	if snaps == nil {
		snaps = []*zfs.Snapshot{}
	}
	h.dsSnaps[ds] = snaps
	delete(h.dsSnapsPend, ds)
}

func (m *Model) ApplyProps(host, ds, text string) {
	if h := m.hostByName(host); h != nil {
		h.dsProps[ds] = zfs.ParseProps(text, ds)
		delete(h.dsPropsPend, ds)
	}
}

// ShowSnaps unfolds a dataset's snapshots into the tree — the `t` toggle,
// ancestors included. A "@label" suffix also unfolds that family (dump
// helper).
func (m *Model) ShowSnaps(host, path string) bool {
	h := m.hostByName(host)
	if h == nil {
		return false
	}
	ds := path
	if i := strings.IndexByte(path, '@'); i >= 0 {
		ds = path[:i]
		m.expanded[treeFamID(h, ds, path[i+1:])] = true
	}
	m.snapsShown[treeDsID(h, ds)] = true
	m.ExpandFor(host, ds)
	return true
}

// MarkSnaps selects snapshots on a dataset (dump helper).
func (m *Model) MarkSnaps(host, ds string, names []string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	m.markOwner = treeDsID(h, ds)
	for _, n := range names {
		n = strings.TrimSpace(strings.TrimPrefix(n, "@"))
		if n != "" {
			m.marks[n] = true
		}
	}
	m.markGen++
}

// SetCursorRow moves the tree cursor to the first visible row matching by
// name: @snap or @family label, dataset base or full path, pool, or host
// (dump helper).
func (m *Model) SetCursorRow(name string) bool {
	name = strings.TrimSpace(name)
	bare := strings.TrimPrefix(name, "@")
	for _, r := range m.treeRows() {
		hit := false
		switch r.kind {
		case rSnap:
			hit = r.snap.Snap == bare
		case rFam:
			hit = r.fam.Label() == bare
		case rDataset:
			hit = r.ds.Base() == name || r.ds.Name == name
		case rPool:
			hit = r.pool.Name == name
		case rHost:
			hit = r.host.name == name
		}
		if hit {
			m.treeSel = r.id
			if r.kind == rPool {
				m.setSel(r.host, r.pool.Name)
			}
			return true
		}
	}
	return false
}

// SelectedFamTarget returns the host and dry-run target when the cursor
// rests on a family row (for --dump prefetching).
func (m *Model) SelectedFamTarget() (string, string) {
	if r := m.treeSelected(); r.kind == rFam {
		return r.host.name, famTarget(r.ds.Name, r.fam)
	}
	return "", ""
}
