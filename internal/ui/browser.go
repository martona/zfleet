package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The drill browser: one level per view, mc-style. Row zero is the
// container itself — the named, inspectable ".." (Enter on it goes up).
// Child datasets indent beneath it; the container's own snapshots close the
// list back at container indent, because they belong to it, not to the
// children. With remotes registered there is one more level above the pool
// root: the host, whose children are its pools. Dataset trees, snapshots,
// and properties live in per-host caches shared with the tree screen.

const (
	modePools = iota // the tree/overview screen
	modeBrowser
)

const datasetsInterval = 10 * time.Second

const familyMinSize = 3

type brLevel struct {
	name   string // container dataset full name
	cursor string // row identity, restored when returning to this level
}

// dryResult caches one `zfs destroy -nv` output, keyed by its exact target
// string — a changed selection or snapshot set is a different key, so the
// cache self-invalidates.
type dryResult struct {
	text    string
	errText string
	pending bool
}

type browserState struct {
	host       *hostState
	pool       string          // "" while on the host level
	stack      []brLevel       // empty = host level (multi-host only)
	hostCursor string          // cursor memory for the host level
	expFams    map[string]bool // container + "\x00" + family label
	sortUsed   bool
	filter     string
	filterIn   bool

	// snapshot multi-select, scoped to the current level; cleared on any
	// level change so there is never an undo fight
	selSnaps map[string]bool // snap short names within the container
	selGen   int             // bumped per change; debounces the dry-run
}

func newBrowserState(h *hostState, pool string) browserState {
	return browserState{
		host:       h,
		pool:       pool,
		hostCursor: "·",
		expFams:    map[string]bool{},
		selSnaps:   map[string]bool{},
		sortUsed:   true,
	}
}

// poolOf returns the pool component of a dataset path.
func poolOf(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// row kinds
const (
	eSelf = iota
	eChild
	eFam
	eSnap
	eHostSelf // host level: the host itself
	ePool     // host level: one of its pools
)

type brEntry struct {
	kind   int
	ds     *zfs.Dataset
	fam    *zfs.SnapFamily
	snap   *zfs.Snapshot
	pool   *zfs.Pool
	id     string
	member bool // snapshot shown scattered from an expanded family
}

// brAtHostLevel reports whether the browser is on the pools-of-a-host level.
func (m *Model) brAtHostLevel() bool {
	return m.mode == modeBrowser && len(m.br.stack) == 0 && m.br.host != nil
}

func (m *Model) brContainer() *zfs.Dataset {
	if m.br.host == nil || len(m.br.stack) == 0 {
		return nil
	}
	tree := m.br.host.dsTrees[m.br.pool]
	if tree == nil {
		return nil
	}
	return tree.ByName[m.br.stack[len(m.br.stack)-1].name]
}

func (m *Model) brEntries() []brEntry {
	filter := strings.ToLower(m.br.filter)
	match := func(s string) bool {
		return filter == "" || strings.Contains(strings.ToLower(s), filter)
	}

	if m.brAtHostLevel() {
		h := m.br.host
		out := []brEntry{{kind: eHostSelf, id: "·"}}
		pools := append([]*zfs.Pool(nil), h.pools...)
		if m.br.sortUsed {
			sort.SliceStable(pools, func(i, j int) bool { return pools[i].Alloc > pools[j].Alloc })
		} else {
			sort.SliceStable(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
		}
		for _, p := range pools {
			if match(p.Name) {
				out = append(out, brEntry{kind: ePool, pool: p, id: p.Name})
			}
		}
		return out
	}

	c := m.brContainer()
	if c == nil {
		return nil
	}
	out := []brEntry{{kind: eSelf, ds: c, id: "·"}}

	kids := append([]*zfs.Dataset(nil), c.Children...)
	if m.br.sortUsed {
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].Used > kids[j].Used })
	} else {
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
	}
	for _, k := range kids {
		if match(k.Base()) {
			out = append(out, brEntry{kind: eChild, ds: k, id: k.Name})
		}
	}

	// snapshots are strictly chronological: a family row sits at the slot
	// its earliest member earned; expanding it scatters the members to
	// their own chronological positions among the named snapshots
	type cand struct {
		t int64
		e brEntry
	}
	var cands []cand
	for _, e := range zfs.GroupSnapshots(m.br.host.dsSnaps[c.Name], familyMinSize) {
		if e.Fam != nil {
			if !match(e.Fam.Label()) {
				continue
			}
			cands = append(cands, cand{e.Fam.Oldest().Creation,
				brEntry{kind: eFam, fam: e.Fam, id: "@" + e.Fam.Label()}})
			if m.br.expFams[c.Name+"\x00"+e.Fam.Label()] {
				for _, s := range e.Fam.Snaps {
					cands = append(cands, cand{s.Creation,
						brEntry{kind: eSnap, snap: s, id: "@" + s.Snap, member: true}})
				}
			}
		} else if match(e.Snap.Snap) {
			cands = append(cands, cand{e.Snap.Creation,
				brEntry{kind: eSnap, snap: e.Snap, id: "@" + e.Snap.Snap}})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
	for _, cd := range cands {
		out = append(out, cd.e)
	}
	return out
}

// famTarget builds the dry-run target for a whole family.
func (m *Model) famTarget(f *zfs.SnapFamily) string {
	c := m.brContainer()
	if c == nil || len(f.Snaps) == 0 {
		return ""
	}
	names := make([]string, len(f.Snaps))
	for i, s := range f.Snaps {
		names[i] = s.Snap
	}
	return c.Name + "@" + strings.Join(names, ",")
}

func (m *Model) brCursorID() string {
	if len(m.br.stack) == 0 {
		return m.br.hostCursor
	}
	return m.br.stack[len(m.br.stack)-1].cursor
}

func (m *Model) brCursorIdx(entries []brEntry) int {
	want := m.brCursorID()
	for i, e := range entries {
		if e.id == want {
			return i
		}
	}
	return 0
}

func (m *Model) brSelected() brEntry {
	e := m.brEntries()
	if len(e) == 0 {
		return brEntry{kind: -1}
	}
	return e[m.brCursorIdx(e)]
}

func (m *Model) brSetCursorID(id string) {
	if len(m.br.stack) == 0 {
		m.br.hostCursor = id
		return
	}
	m.br.stack[len(m.br.stack)-1].cursor = id
}

func (m *Model) brMove(delta int) {
	e := m.brEntries()
	if len(e) == 0 {
		return
	}
	i := m.brCursorIdx(e) + delta
	if i < 0 {
		i = 0
	}
	if i > len(e)-1 {
		i = len(e) - 1
	}
	m.brSetCursorID(e[i].id)
}

func (m *Model) brClearSelection() {
	if len(m.br.selSnaps) > 0 {
		m.br.selSnaps = map[string]bool{}
		m.br.selGen++
	}
}

func (m *Model) brUp() {
	m.brClearSelection()
	switch {
	case len(m.br.stack) > 1:
		m.br.stack = m.br.stack[:len(m.br.stack)-1]
	case len(m.br.stack) == 1 && m.multiHost:
		// pool root → the host level, cursor on the pool we came from
		m.br.hostCursor = m.br.pool
		m.br.stack = nil
		m.br.pool = ""
	default:
		m.mode = modePools
	}
	m.br.filter, m.br.filterIn = "", false
}

func (m *Model) brDescend(ds *zfs.Dataset) {
	m.brClearSelection()
	m.br.stack = append(m.br.stack, brLevel{name: ds.Name, cursor: "·"})
	m.br.filter, m.br.filterIn = "", false
}

// brDescendPool enters a pool's root level from the host level.
func (m *Model) brDescendPool(pool string) tea.Cmd {
	m.brClearSelection()
	m.br.pool = pool
	m.br.stack = []brLevel{{name: pool, cursor: "·"}}
	m.br.filter, m.br.filterIn = "", false
	return tea.Batch(m.ensureTreeCmd(m.br.host, pool), m.brEnsure())
}

// brToggleSel toggles selection on the current row: a snapshot toggles
// itself, a family toggles all members together.
func (m *Model) brToggleSel() bool {
	switch sel := m.brSelected(); sel.kind {
	case eSnap:
		if m.br.selSnaps[sel.snap.Snap] {
			delete(m.br.selSnaps, sel.snap.Snap)
		} else {
			m.br.selSnaps[sel.snap.Snap] = true
		}
	case eFam:
		all := true
		for _, s := range sel.fam.Snaps {
			if !m.br.selSnaps[s.Snap] {
				all = false
				break
			}
		}
		for _, s := range sel.fam.Snaps {
			if all {
				delete(m.br.selSnaps, s.Snap)
			} else {
				m.br.selSnaps[s.Snap] = true
			}
		}
	default:
		return false
	}
	m.br.selGen++
	return true
}

// brSelection returns the selected snapshots in chronological order,
// intersected with the container's current snapshot list.
func (m *Model) brSelection() []*zfs.Snapshot {
	c := m.brContainer()
	if c == nil || len(m.br.selSnaps) == 0 {
		return nil
	}
	var out []*zfs.Snapshot
	for _, s := range m.br.host.dsSnaps[c.Name] {
		if m.br.selSnaps[s.Snap] {
			out = append(out, s)
		}
	}
	return out
}

// SelectionTarget builds the `zfs destroy -n` argument ("ds@a,b,c").
func (m *Model) SelectionTarget() string {
	sel := m.brSelection()
	if len(sel) == 0 {
		return ""
	}
	names := make([]string, len(sel))
	for i, s := range sel {
		names[i] = s.Snap
	}
	return m.brContainer().Name + "@" + strings.Join(names, ",")
}

// enterBrowserAt opens the drill browser on a host: at a dataset path, at a
// pool's root level (bare pool name), or at the host level (empty path).
// Returning to the same spot restores the previous position.
func (m *Model) enterBrowserAt(h *hostState, path string) tea.Cmd {
	if path == "" {
		if m.br.host != h {
			m.br = newBrowserState(h, "")
		} else {
			m.brClearSelection()
			m.br.stack = nil
			m.br.pool = ""
			m.br.filter, m.br.filterIn = "", false
		}
		m.mode = modeBrowser
		return m.brEnsure()
	}
	pool := poolOf(path)
	samePool := m.br.host == h && m.br.pool == pool
	if !samePool {
		m.br = newBrowserState(h, pool)
	}
	if !(samePool && path == pool && len(m.br.stack) > 0) {
		segs := strings.Split(path, "/")
		m.br.stack = nil
		for i := range segs {
			m.br.stack = append(m.br.stack, brLevel{name: strings.Join(segs[:i+1], "/"), cursor: "·"})
		}
	}
	m.mode = modeBrowser
	return tea.Batch(m.ensureTreeCmd(h, pool), m.brEnsure())
}

// lazy-fetch helpers shared by the browser and the tree screen

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
func (m *Model) ensureDryCmd(target string) tea.Cmd {
	h := m.br.host
	if target == "" || h == nil {
		return nil
	}
	if _, ok := h.dryCache[target]; ok {
		return nil
	}
	h.dryCache[target] = &dryResult{pending: true}
	return fetchDryRun(h, target)
}

// brEnsure requests any lazy data the current view is missing.
func (m *Model) brEnsure() tea.Cmd {
	h := m.br.host
	c := m.brContainer()
	if c == nil {
		return nil
	}
	cmds := []tea.Cmd{m.ensureSnapsCmd(h, c.Name)}
	switch sel := m.brSelected(); {
	case sel.ds != nil:
		cmds = append(cmds, m.ensureSnapsCmd(h, sel.ds.Name), m.ensurePropsCmd(h, sel.ds.Name))
	case sel.kind == eFam:
		// don't tell the user a dry-run is needed — just run it
		cmds = append(cmds, m.ensureDryCmd(m.famTarget(sel.fam)))
	}
	return tea.Batch(cmds...)
}

func (m *Model) browserKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.br.filterIn {
		switch msg.String() {
		case "esc":
			m.br.filter, m.br.filterIn = "", false
		case "enter":
			m.br.filterIn = false
		case "backspace":
			if r := []rune(m.br.filter); len(r) > 0 {
				m.br.filter = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes || msg.String() == " " {
				m.br.filter += msg.String()
			}
		}
		return m, m.brEnsure()
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "down":
		m.brMove(1)
	case "up":
		m.brMove(-1)
	case "j":
		m.panelScroll++
	case "k":
		m.panelScroll--
	case "g":
		if e := m.brEntries(); len(e) > 0 {
			m.brSetCursorID(e[0].id)
		}
	case "G":
		if e := m.brEntries(); len(e) > 0 {
			m.brSetCursorID(e[len(e)-1].id)
		}
	case "enter":
		switch sel := m.brSelected(); sel.kind {
		case eHostSelf: // the host's own ".." — back out to the tree
			m.mode = modePools
		case ePool:
			return m, m.brDescendPool(sel.pool.Name)
		case eSelf: // the named ".." — Enter walks up
			m.brUp()
		case eChild:
			m.brDescend(sel.ds)
		case eFam:
			key := m.brContainer().Name + "\x00" + sel.fam.Label()
			m.br.expFams[key] = !m.br.expFams[key]
		}
	case "backspace", "left", "h":
		if m.brAtHostLevel() {
			m.mode = modePools
			break
		}
		m.brUp()
	case "esc":
		switch {
		case len(m.br.selSnaps) > 0:
			m.brClearSelection()
		case m.br.filter != "":
			m.br.filter = ""
		case m.brAtHostLevel():
			m.mode = modePools
		default:
			m.brUp()
		}
	case "s":
		m.br.sortUsed = !m.br.sortUsed
	case "/":
		m.br.filterIn = true
	case " ":
		if m.brToggleSel() {
			m.brMove(1)
			return m, tea.Batch(m.brEnsure(), m.brDryRunDebounce())
		}
	case "shift+down":
		if m.brToggleSel() {
			m.brMove(1)
			return m, tea.Batch(m.brEnsure(), m.brDryRunDebounce())
		}
	case "shift+up":
		if m.brToggleSel() {
			m.brMove(-1)
			return m, tea.Batch(m.brEnsure(), m.brDryRunDebounce())
		}
	}
	return m, m.brEnsure()
}

// brDryRunDebounce schedules the reclaim computation a beat after the last
// selection change, so spacebar streaks cost one exec, not one per press.
func (m *Model) brDryRunDebounce() tea.Cmd {
	gen := m.br.selGen
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return dryTickMsg{gen: gen} })
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

// BrowseTo opens the browser at the given dataset's level on a host; an
// empty path opens the host level (dump helper).
func (m *Model) BrowseTo(host, path string) bool {
	h := m.hostByName(host)
	if h == nil {
		return false
	}
	if path == "" {
		m.br = newBrowserState(h, "")
		m.mode = modeBrowser
		return true
	}
	pool := poolOf(path)
	tree := h.dsTrees[pool]
	if tree == nil || tree.ByName[path] == nil {
		return false
	}
	if m.br.host != h || m.br.pool != pool {
		m.br = newBrowserState(h, pool)
	}
	segs := strings.Split(path, "/")
	m.br.stack = nil
	for i := range segs {
		m.br.stack = append(m.br.stack, brLevel{name: strings.Join(segs[:i+1], "/"), cursor: "·"})
	}
	m.mode = modeBrowser
	return true
}

// SelectedFamTarget returns the dry-run target when the cursor rests on a
// family row (for --dump prefetching).
func (m *Model) SelectedFamTarget() string {
	if sel := m.brSelected(); sel.kind == eFam {
		return m.famTarget(sel.fam)
	}
	return ""
}

// SelectedDatasetName returns the full name of the selected dataset row, or
// "" when the selection is not a dataset (for --dump prefetching).
func (m *Model) SelectedDatasetName() string {
	if sel := m.brSelected(); sel.ds != nil {
		return sel.ds.Name
	}
	return ""
}

// SetCursorRow moves the cursor to the row whose base name or @snap-name
// matches (for --dump --cursor).
func (m *Model) SetCursorRow(name string) bool {
	for _, e := range m.brEntries() {
		hit := e.id == name ||
			(e.ds != nil && e.ds.Base() == name) ||
			(e.snap != nil && e.snap.Snap == name) ||
			(e.fam != nil && e.fam.Label() == name) ||
			(e.pool != nil && e.pool.Name == name)
		if hit {
			m.brSetCursorID(e.id)
			return true
		}
	}
	return false
}
