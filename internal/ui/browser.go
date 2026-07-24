package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/zfs"
)

// The dataset browser: one level per view, mc-style. Row zero is the
// container itself — the named, inspectable ".." (Enter on it goes up).
// Child datasets indent beneath it; the container's own snapshots close the
// list back at container indent, because they belong to it, not to the
// children.

const (
	modePools = iota
	modeBrowser
)

const browserInterval = 10 * time.Second

const familyMinSize = 3

type brLevel struct {
	name   string // container dataset full name
	cursor string // row identity, restored when returning to this level
}

type browserState struct {
	pool      string
	tree      *zfs.DatasetTree
	stack     []brLevel
	snaps     map[string][]*zfs.Snapshot // fetched; empty non-nil = "none"
	snapsPend map[string]bool
	props     map[string]map[string]zfs.Prop
	propsPend map[string]bool
	expanded  map[string]bool // container + "\x00" + family label
	sortUsed  bool
	filter    string
	filterIn  bool
	tickArmed bool
}

func newBrowserState(pool string) browserState {
	return browserState{
		pool:      pool,
		snaps:     map[string][]*zfs.Snapshot{},
		snapsPend: map[string]bool{},
		props:     map[string]map[string]zfs.Prop{},
		propsPend: map[string]bool{},
		expanded:  map[string]bool{},
		sortUsed:  true,
	}
}

// row kinds
const (
	eSelf = iota
	eChild
	eFam
	eSnap
)

type brEntry struct {
	kind int
	ds   *zfs.Dataset
	fam  *zfs.SnapFamily
	snap *zfs.Snapshot
	id   string
}

func (m *Model) brContainer() *zfs.Dataset {
	if m.br.tree == nil || len(m.br.stack) == 0 {
		return nil
	}
	return m.br.tree.ByName[m.br.stack[len(m.br.stack)-1].name]
}

func (m *Model) brEntries() []brEntry {
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
	filter := strings.ToLower(m.br.filter)
	match := func(s string) bool {
		return filter == "" || strings.Contains(strings.ToLower(s), filter)
	}
	for _, k := range kids {
		if match(k.Base()) {
			out = append(out, brEntry{kind: eChild, ds: k, id: k.Name})
		}
	}

	snapEntries := zfs.GroupSnapshots(m.br.snaps[c.Name], familyMinSize)
	if m.br.sortUsed {
		sort.SliceStable(snapEntries, func(i, j int) bool { return snapEntries[i].Used() > snapEntries[j].Used() })
	}
	for _, e := range snapEntries {
		if e.Fam != nil {
			if !match(e.Fam.Label()) {
				continue
			}
			out = append(out, brEntry{kind: eFam, fam: e.Fam, id: "@" + e.Fam.Label()})
			if m.br.expanded[c.Name+"\x00"+e.Fam.Label()] {
				for _, s := range e.Fam.Snaps {
					out = append(out, brEntry{kind: eSnap, snap: s, id: "@" + s.Snap})
				}
			}
		} else if match(e.Snap.Snap) {
			out = append(out, brEntry{kind: eSnap, snap: e.Snap, id: "@" + e.Snap.Snap})
		}
	}
	return out
}

func (m *Model) brCursorIdx(entries []brEntry) int {
	want := m.br.stack[len(m.br.stack)-1].cursor
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

func (m *Model) brUp() {
	if len(m.br.stack) > 1 {
		m.br.stack = m.br.stack[:len(m.br.stack)-1]
	} else {
		m.mode = modePools
	}
	m.br.filter, m.br.filterIn = "", false
}

func (m *Model) brDescend(ds *zfs.Dataset) {
	m.br.stack = append(m.br.stack, brLevel{name: ds.Name, cursor: "·"})
	m.br.filter, m.br.filterIn = "", false
}

// enterBrowser switches to the dataset browser for a pool, reusing cached
// state when returning to the same pool.
func (m *Model) enterBrowser(pool string) tea.Cmd {
	if m.br.pool != pool {
		m.br = newBrowserState(pool)
	}
	if len(m.br.stack) == 0 {
		m.br.stack = []brLevel{{name: pool, cursor: "·"}}
	}
	m.mode = modeBrowser
	cmds := []tea.Cmd{fetchDatasets(m.src, pool)}
	if !m.br.tickArmed {
		m.br.tickArmed = true
		cmds = append(cmds, tea.Tick(browserInterval, func(time.Time) tea.Msg { return browserTickMsg{} }))
	}
	return tea.Batch(cmds...)
}

// brEnsure requests any lazy data the current view is missing: the
// container's snapshots (they are rows) and the selection's snapshots and
// property sources (they feed the inspector).
func (m *Model) brEnsure() tea.Cmd {
	c := m.brContainer()
	if c == nil {
		return nil
	}
	var cmds []tea.Cmd
	needSnaps := func(name string) {
		if _, ok := m.br.snaps[name]; !ok && !m.br.snapsPend[name] {
			m.br.snapsPend[name] = true
			cmds = append(cmds, fetchSnaps(m.src, name))
		}
	}
	needProps := func(name string) {
		if _, ok := m.br.props[name]; !ok && !m.br.propsPend[name] {
			m.br.propsPend[name] = true
			cmds = append(cmds, fetchProps(m.src, name))
		}
	}
	needSnaps(c.Name)
	if sel := m.brSelected(); sel.ds != nil {
		needSnaps(sel.ds.Name)
		needProps(sel.ds.Name)
	}
	if len(cmds) == 0 {
		return nil
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
	case "down", "j":
		m.brMove(1)
	case "up", "k":
		m.brMove(-1)
	case "g":
		if e := m.brEntries(); len(e) > 0 {
			m.brSetCursorID(e[0].id)
		}
	case "G":
		if e := m.brEntries(); len(e) > 0 {
			m.brSetCursorID(e[len(e)-1].id)
		}
	case "enter", "l", "right":
		switch sel := m.brSelected(); sel.kind {
		case eSelf:
			if msg.String() == "enter" { // the named ".." — Enter walks up
				m.brUp()
			}
		case eChild:
			m.brDescend(sel.ds)
		case eFam:
			key := m.brContainer().Name + "\x00" + sel.fam.Label()
			m.br.expanded[key] = !m.br.expanded[key]
		}
	case "backspace", "left", "h":
		m.brUp()
	case "esc":
		if m.br.filter != "" {
			m.br.filter = ""
		} else {
			m.brUp()
		}
	case "s":
		m.br.sortUsed = !m.br.sortUsed
	case "/":
		m.br.filterIn = true
	}
	return m, m.brEnsure()
}

// messages and fetch commands

type datasetsMsg struct {
	pool string
	text string
	err  error
}
type snapsMsg struct {
	ds   string
	text string
	err  error
}
type propsMsg struct {
	ds   string
	text string
	err  error
}
type browserTickMsg struct{}

func fetchDatasets(src collect.Source, pool string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.DatasetTexts(ctx, pool)
		return datasetsMsg{pool: pool, text: text, err: err}
	}
}

func fetchSnaps(src collect.Source, ds string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.SnapshotTexts(ctx, ds)
		return snapsMsg{ds: ds, text: text, err: err}
	}
}

func fetchProps(src collect.Source, ds string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.PropTexts(ctx, ds)
		return propsMsg{ds: ds, text: text, err: err}
	}
}

// exported helpers for --dump

func (m *Model) ApplyDatasets(pool, text string) {
	if m.br.pool != pool {
		m.br = newBrowserState(pool)
	}
	m.br.tree = zfs.ParseDatasets(text)
}

func (m *Model) ApplySnaps(ds, text string) {
	snaps := zfs.ParseSnapshots(text, ds)
	if snaps == nil {
		snaps = []*zfs.Snapshot{}
	}
	m.br.snaps[ds] = snaps
	delete(m.br.snapsPend, ds)
}

func (m *Model) ApplyProps(ds, text string) {
	m.br.props[ds] = zfs.ParseProps(text, ds)
	delete(m.br.propsPend, ds)
}

// BrowseTo opens the browser at the given dataset's level.
func (m *Model) BrowseTo(path string) bool {
	if m.br.tree == nil || m.br.tree.ByName[path] == nil {
		return false
	}
	segs := strings.Split(path, "/")
	m.br.stack = nil
	for i := range segs {
		m.br.stack = append(m.br.stack, brLevel{name: strings.Join(segs[:i+1], "/"), cursor: "·"})
	}
	m.mode = modeBrowser
	return true
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
			(e.fam != nil && e.fam.Label() == name)
		if hit {
			m.brSetCursorID(e.id)
			return true
		}
	}
	return false
}
