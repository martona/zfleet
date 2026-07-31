package ui

import (
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfleet/internal/zfs"
)

// The tree screen: one expandable tree — overview row, hosts (when any
// remote is registered), pools, as much of the dataset hierarchy as the
// user has unfolded, and snapshots as FULL CITIZENS: a dataset with
// snapshots earns a real chevron and → unfolds them like children —
// children first, snapshots after (they are more numerous and less
// interesting — Marton's ordering). `t` is the opt-out: it folds an open
// dataset's snapshots away (children stay) and brings them back. Chevron
// knowledge comes from the pool-recursive snapshot sweep (the / filter's
// machinery) fired when a pool expands; until it lands, childless
// datasets wear a dim · placeholder. Two presentations of the same rows:
// cursor on "≡ overview" drops the inspector and renders full-width with
// io columns; cursor anywhere else is the classic two-pane browse.
// →/Enter expands in place, ← collapses (or jumps to parent). Host rows
// are organizational anchors: always open, never collapsible.

const overviewID = "≡"

const (
	rOverview = iota
	rHost
	rPool
	rDataset
	rFam
	rSnap
	rPending // data in flight — the row that keeps Enter feeling instant
)

type treeRow struct {
	kind       int
	host       *hostState
	pool       *zfs.Pool
	ds         *zfs.Dataset // rDataset: the row itself; rFam/rSnap: the owning dataset
	fam        *zfs.SnapFamily
	snap       *zfs.Snapshot
	member     bool // snapshot scattered from an unfolded family
	hit        bool // filter views: a genuine match, not a structural ancestor
	sel        bool // marked, directly or by inheritance from a marked ancestor
	depth      int  // dataset rows: 1 = root dataset; snap rows: owner depth + 1
	id         string
	parentID   string
	expandable bool
	expanded   bool
	// snapshot census still in flight: the chevron slot shows a dim ·
	// until the pool sweep says whether this childless dataset earns one
	chevUnknown bool
	// expansion holds ONLY snapshots — the chevron wears the snapshot
	// violet so nobody gets jebaited into finding no datasets inside
	snapOnly bool
}

// row ids are host-qualified so identical pool names on different hosts
// stay distinct; \x00 can appear in neither. A snapshot row's id is simply
// the dataset id of its full zfs name (ds@snap).
func treeHostID(h *hostState) string              { return "h:" + h.name }
func treePoolID(h *hostState, name string) string { return "p:" + h.name + "\x00" + name }
func treeDsID(h *hostState, name string) string   { return h.name + "\x00" + name }
func treeFamID(h *hostState, ds, label string) string {
	return "f:" + h.name + "\x00" + ds + "\x00" + label
}

// splitPoolID recovers (host, pool) from a pool row id.
func splitPoolID(m *Model, id string) (*hostState, string, bool) {
	if !strings.HasPrefix(id, "p:") {
		return nil, "", false
	}
	rest := strings.TrimPrefix(id, "p:")
	i := strings.IndexByte(rest, 0)
	if i < 0 {
		return nil, "", false
	}
	h := m.hostByName(rest[:i])
	if h == nil {
		return nil, "", false
	}
	return h, rest[i+1:], true
}

// treeRows serves the current row list from the per-cycle cache — frame
// composition asks for it many times per keypress, and on a big fleet the
// walk is the whole cost of a key.
func (m *Model) treeRows() []treeRow {
	if !m.rowsOK {
		m.rowsCache = m.buildRows()
		m.rowsOK = true
	}
	return m.rowsCache
}

func (m *Model) buildRows() []treeRow {
	if m.filter != "" {
		return m.filterRows()
	}
	rows := []treeRow{{kind: rOverview, id: overviewID}}
	for _, h := range m.hosts {
		if m.multiHost {
			rows = append(rows, treeRow{kind: rHost, host: h, id: treeHostID(h)})
		}
		for _, p := range h.pools {
			pid := treePoolID(h, p.Name)
			parent := ""
			if m.multiHost {
				parent = treeHostID(h)
			}
			rows = append(rows, treeRow{
				kind: rPool, host: h, pool: p, id: pid, parentID: parent,
				expandable: true, expanded: m.expanded[pid],
			})
			if !m.expanded[pid] {
				continue
			}
			tree := h.dsTrees[p.Name]
			if tree == nil {
				// fetch in flight — answer the keypress NOW, swap in the
				// real rows when the data lands
				rows = append(rows, treeRow{kind: rPending, host: h, pool: p,
					depth: 1, id: "w:" + pid, parentID: pid})
				continue
			}
			root := tree.ByName[p.Name]
			if root == nil {
				continue
			}
			var walk func(d *zfs.Dataset, depth int, parent string, cov bool)
			walk = func(d *zfs.Dataset, depth int, parent string, cov bool) {
				id := treeDsID(h, d.Name)
				exp := m.expanded[id]
				sel := cov || m.marks[id]
				hasSnaps, known := h.snapState(d.Name)
				unknown := !known
				rows = append(rows, treeRow{
					kind: rDataset, host: h, ds: d, depth: depth, id: id, parentID: parent,
					expandable:  len(d.Children) > 0 || hasSnaps || unknown,
					chevUnknown: len(d.Children) == 0 && unknown,
					snapOnly:    len(d.Children) == 0 && hasSnaps,
					expanded:    exp, sel: sel,
				})
				if !exp {
					return
				}
				kids := append([]*zfs.Dataset(nil), d.Children...)
				if m.treeSortUsed {
					sort.SliceStable(kids, func(i, j int) bool { return kids[i].Used > kids[j].Used })
				} else {
					sort.SliceStable(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
				}
				for _, k := range kids {
					walk(k, depth+1, id, sel)
				}
				// snapshots AFTER the children — more numerous, less
				// interesting — unless t folded them out of this view
				if m.snapsFolded[id] {
					return
				}
				switch {
				case hasSnaps:
					rows = append(rows, m.snapRows(h, d, depth+1, id, sel)...)
				case unknown:
					rows = append(rows, treeRow{kind: rPending, host: h, ds: d,
						depth: depth + 1, id: "w:" + id, parentID: id})
				}
			}
			walk(root, 1, pid, false)
		}
	}
	return rows
}

// filterRows is the tree pruned to the active filter: hosts stay as
// anchors, the path to each match stays as dim structure, matches render
// bright, everything else is gone. Matching snapshots appear flat —
// search results are a set, not a browse.
//
// The ds part addresses dataset NAMES — the tree already shows lineage, so
// a child silently inheriting its parent's path is not a match. Typing a
// '/' in the pattern is the explicit switch to full-path matching
// ("rust/recv/*@x" scopes a subtree on purpose).
func (m *Model) filterRows() []treeRow {
	dsPat, snapPat, hasSnap := splitFilter(m.filter)
	dsByPath := strings.Contains(dsPat, "/")
	rows := []treeRow{{kind: rOverview, id: overviewID}}
	for _, h := range m.hosts {
		if m.multiHost {
			rows = append(rows, treeRow{kind: rHost, host: h, id: treeHostID(h)})
		}
		for _, p := range h.pools {
			tree := h.dsTrees[p.Name]
			if tree == nil {
				continue // sweep in flight; the title carries the count
			}
			root := tree.ByName[p.Name]
			if root == nil {
				continue
			}
			pid := treePoolID(h, p.Name)
			parent := ""
			if m.multiHost {
				parent = treeHostID(h)
			}
			var walk func(d *zfs.Dataset, depth int, parentID string, cov bool) []treeRow
			walk = func(d *zfs.Dataset, depth int, parentID string, cov bool) []treeRow {
				id := treeDsID(h, d.Name)
				sel := cov || m.marks[id]
				target := d.Base()
				if dsByPath {
					target = d.Name
				}
				hit := filterMatch(target, dsPat)
				var snaps []*zfs.Snapshot
				if hasSnap && hit {
					for _, s := range h.dsSnaps[d.Name] {
						if filterMatch(s.Snap, snapPat) {
							snaps = append(snaps, s)
						}
					}
					hit = len(snaps) > 0
				}
				kids := append([]*zfs.Dataset(nil), d.Children...)
				if m.treeSortUsed {
					sort.SliceStable(kids, func(i, j int) bool { return kids[i].Used > kids[j].Used })
				} else {
					sort.SliceStable(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
				}
				var kidRows []treeRow
				for _, k := range kids {
					kidRows = append(kidRows, walk(k, depth+1, id, sel)...)
				}
				if !hit && len(kidRows) == 0 {
					return nil
				}
				out := []treeRow{{kind: rDataset, host: h, ds: d, depth: depth,
					id: id, parentID: parentID, hit: hit, sel: sel}}
				snapCov := sel || m.marks[id+"@"]
				for _, s := range snaps {
					sid := treeDsID(h, d.Name+"@"+s.Snap)
					out = append(out, treeRow{kind: rSnap, host: h, ds: d, snap: s,
						hit: true, depth: depth + 1, sel: snapCov || m.marks[sid],
						id: sid, parentID: id})
				}
				return append(out, kidRows...)
			}
			dsRows := walk(root, 1, pid, false)
			if len(dsRows) == 0 {
				continue
			}
			rows = append(rows, treeRow{kind: rPool, host: h, pool: p,
				id: pid, parentID: parent, hit: true})
			rows = append(rows, dsRows...)
		}
	}
	return rows
}

// filterHits counts the matches the title reports: snapshots when the
// pattern hunts snapshots, datasets otherwise.
func (m *Model) filterHits(rows []treeRow) int {
	_, _, hasSnap := splitFilter(m.filter)
	n := 0
	for _, r := range rows {
		if r.hit && ((hasSnap && r.kind == rSnap) || (!hasSnap && r.kind == rDataset)) {
			n++
		}
	}
	return n
}

// snapRows builds a dataset's snapshot rows. Strictly chronological: a
// family row sits at the slot its earliest member earned; unfolding it
// scatters the members to their own chronological positions among the named
// snapshots. cov marks the whole set selected-by-inheritance.
func (m *Model) snapRows(h *hostState, d *zfs.Dataset, depth int, parent string, cov bool) []treeRow {
	cov = cov || m.marks[treeDsID(h, d.Name)+"@"] // the all-snaps pseudo
	snapSel := func(id string) bool { return cov || m.marks[id] }
	type cand struct {
		t int64
		r treeRow
	}
	var cands []cand
	for _, e := range zfs.GroupSnapshots(h.dsSnaps[d.Name], familyMinSize) {
		if e.Fam != nil {
			fid := treeFamID(h, d.Name, e.Fam.Label())
			exp := m.expanded[fid]
			all := true
			for _, s := range e.Fam.Snaps {
				if !snapSel(treeDsID(h, d.Name+"@"+s.Snap)) {
					all = false
					break
				}
			}
			cands = append(cands, cand{e.Fam.Oldest().Creation, treeRow{
				kind: rFam, host: h, ds: d, fam: e.Fam, depth: depth,
				id: fid, parentID: parent, expandable: true, expanded: exp, sel: all,
			}})
			if !exp {
				continue
			}
			for _, s := range e.Fam.Snaps {
				id := treeDsID(h, d.Name+"@"+s.Snap)
				cands = append(cands, cand{s.Creation, treeRow{
					kind: rSnap, host: h, ds: d, snap: s, member: true, depth: depth,
					id: id, parentID: parent, sel: snapSel(id),
				}})
			}
		} else {
			id := treeDsID(h, d.Name+"@"+e.Snap.Snap)
			cands = append(cands, cand{e.Snap.Creation, treeRow{
				kind: rSnap, host: h, ds: d, snap: e.Snap, depth: depth,
				id: id, parentID: parent, sel: snapSel(id),
			}})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
	rows := make([]treeRow, len(cands))
	for i, c := range cands {
		rows[i] = c.r
	}
	return rows
}

func (m *Model) treeIdx(rows []treeRow) int {
	for i, r := range rows {
		if r.id == m.treeSel {
			return i
		}
	}
	// a pending row resolved into real rows — land on its parent, not on
	// the overview
	if want := strings.TrimPrefix(m.treeSel, "w:"); want != m.treeSel {
		for i, r := range rows {
			if r.id == want {
				return i
			}
		}
	}
	return 0
}

func (m *Model) treeSelected() treeRow {
	rows := m.treeRows()
	if len(rows) == 0 {
		return treeRow{kind: -1}
	}
	return rows[m.treeIdx(rows)]
}

func (m *Model) treeMove(delta int) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return
	}
	cur := m.treeIdx(rows)
	i := cur + delta
	if i < 0 {
		i = 0
	}
	if i > len(rows)-1 {
		i = len(rows) - 1
	}
	if rows[i].id != m.treeSel {
		// settle-hold — but a hop that stays inside the selection's world
		// keeps the very same collection panel on screen, and blanking an
		// unchanged panel is pure flicker
		if !(m.inMarkContext(rows[cur]) && m.inMarkContext(rows[i])) {
			m.cursorMovedAt = time.Now()
		}
	}
	m.treeSel = rows[i].id
	if rows[i].kind == rPool {
		m.setSel(rows[i].host, rows[i].pool.Name) // resets the pool inspector's elastic widths
	}
}

// treeEnsure lazily fetches whatever the selected row's inspector needs.
func (m *Model) treeEnsure() tea.Cmd {
	switch row := m.treeSelected(); row.kind {
	case rDataset:
		return tea.Batch(m.ensureSnapsCmd(row.host, row.ds.Name),
			m.ensurePropsCmd(row.host, row.ds.Name))
	case rFam:
		// don't tell the user a dry-run is needed — just run it
		return m.ensureDryCmd(row.host, famTarget(row.ds.Name, row.fam))
	}
	return nil
}

// toggleSnapsFold handles `t` — the citizenship opt-out: fold an open
// dataset's snapshots away (children stay), or bring them back. On a
// collapsed dataset it means "show me the snaps": expand with them
// visible. → always clears the fold — t is a view op, not a mode.
func (m *Model) toggleSnapsFold() (tea.Cmd, bool) {
	row := m.treeSelected()
	id := row.id
	switch row.kind {
	case rDataset:
	case rSnap, rFam:
		id = row.parentID
	default:
		return nil, false
	}
	h, ds := row.host, row.ds
	if m.expanded[id] && !m.snapsFolded[id] {
		m.snapsFolded[id] = true
		if len(ds.Children) == 0 {
			// nothing left under an open childless dataset: collapse whole
			delete(m.expanded, id)
			delete(m.snapsFolded, id)
		}
		if row.kind != rDataset {
			m.treeSel = id // the row under the cursor just vanished
		}
		// marks survive the fold — the selection is fleet state, not view
		// state; the collection panel keeps accounting for them
		return nil, true
	}
	if has, known := h.snapState(ds.Name); known && !has {
		return nil, true // known-none: opening an empty ▾ would be a jebait
	}
	delete(m.snapsFolded, id)
	m.expanded[id] = true
	return m.ensureSnapsCmd(h, ds.Name), true
}

func (m *Model) treeKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filterIn {
		switch msg.String() {
		case "esc":
			m.filter, m.filterIn = "", false
		case "enter":
			m.filterIn = false
		case "down", "up", "pgdown", "pgup":
			// the bar is modeless: arrows leave it standing and walk the
			// results it is already showing
			m.filterIn = false
			switch msg.String() {
			case "down":
				m.treeMove(1)
			case "up":
				m.treeMove(-1)
			case "pgdown":
				m.treeMove(m.pageStep())
			case "pgup":
				m.treeMove(-m.pageStep())
			}
			return m, tea.Batch(m.ensureFilterCmd(), m.treeEnsure())
		case "backspace":
			if m.filter == "" {
				m.filterIn = false // an empty bar backspaces itself away
				break
			}
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes || msg.String() == " " {
				m.filter += msg.String()
			}
		}
		return m, m.ensureFilterCmd()
	}

	// any key that is not a select re-arms the flood guard: deliberate
	// re-toggling always has a movement or other key between the spaces
	if s := msg.String(); s != " " && s != "shift+down" && s != "shift+up" {
		m.lastSpace = ""
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "/":
		m.filterIn = true
		return m, m.ensureFilterCmd()
	case "down":
		m.treeMove(1)
	case "up":
		m.treeMove(-1)
	case "pgdown":
		m.treeMove(m.pageStep())
	case "pgup":
		m.treeMove(-m.pageStep())
	case "*":
		if m.bulkToggleMatches() {
			return m, tea.Batch(m.treeEnsure(), m.markDebounce())
		}
	case "j":
		m.panelScroll++
	case "k":
		m.panelScroll--
	case "g":
		m.treeSel = overviewID
		m.cursorMovedAt = time.Now()
	case "G":
		if rows := m.treeRows(); len(rows) > 0 {
			m.treeSel = rows[len(rows)-1].id
			m.cursorMovedAt = time.Now()
		}
	case "right", "l", "enter":
		if m.filter != "" {
			break // the filter owns the structure; navigate or esc out
		}
		row := m.treeSelected()
		if row.expandable && !row.expanded {
			m.expanded[row.id] = true
			switch row.kind {
			case rPool:
				// the sweep is the chevron census: one command answers
				// "which datasets here have snapshots" for the whole pool
				return m, tea.Batch(m.ensureTreeCmd(row.host, row.pool.Name),
					m.ensurePoolSweep(row.host, row.pool.Name), m.treeEnsure())
			case rDataset:
				// → opens everything; t is the per-view opt-out
				delete(m.snapsFolded, row.id)
				return m, tea.Batch(m.ensureSnapsCmd(row.host, row.ds.Name), m.treeEnsure())
			}
		}
	case "left", "h":
		if m.filter != "" {
			break
		}
		row := m.treeSelected()
		switch {
		case row.expanded:
			delete(m.expanded, row.id)
		case row.parentID != "":
			m.treeSel = row.parentID
		}
	case "t":
		if m.filter != "" {
			break
		}
		if cmd, ok := m.toggleSnapsFold(); ok {
			return m, cmd
		}
	case " ", "shift+down":
		row := m.treeSelected()
		if row.id == m.lastSpace {
			break // buffered flood on an unmoved cursor: one toggle counted
		}
		if m.filter != "" && !m.filterMarkable(row) {
			m.treeMove(1) // chrome, not a match: skip, keep the streak flowing
			break
		}
		if m.toggleMark(row) {
			m.lastSpace = row.id
			m.treeMove(1)
			return m, tea.Batch(m.treeEnsure(), m.markDebounce())
		}
	case "shift+up":
		row := m.treeSelected()
		if row.id == m.lastSpace {
			break
		}
		if m.filter != "" && !m.filterMarkable(row) {
			m.treeMove(-1)
			break
		}
		if m.toggleMark(row) {
			m.lastSpace = row.id
			m.treeMove(-1)
			return m, tea.Batch(m.treeEnsure(), m.markDebounce())
		}
	case "v":
		m.verboseDrives = !m.verboseDrives
	case "a":
		m.OpenAckPopup()
	case "f8":
		if len(m.marks) > 0 {
			m.OpenDestroyPopup()
		}
	case "esc":
		switch {
		case len(m.marks) > 0:
			m.clearMarks()
		case m.filter != "":
			// land where the hunt ended: unfold the real tree down to the
			// row under the cursor before dropping the filter
			if row := m.treeSelected(); row.kind == rDataset || row.kind == rSnap {
				m.ExpandFor(row.host.name, row.ds.Name)
				if row.kind == rSnap {
					// landing on a snap: its dataset expands with snaps showing
					delete(m.snapsFolded, treeDsID(row.host, row.ds.Name))
					m.expanded[treeDsID(row.host, row.ds.Name)] = true
				}
			}
			m.filter = ""
		}
	case "s":
		m.treeSortUsed = !m.treeSortUsed
	}
	return m, m.treeEnsure()
}

// pageStep is one screenful of rows for pgup/pgdn.
func (m *Model) pageStep() int {
	if s := m.h - 6; s > 5 {
		return s
	}
	return 5
}

// ExpandFor unfolds the tree down to the given pool or dataset path on a
// host (dump helper; ancestors expand too).
func (m *Model) ExpandFor(host, path string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	m.expanded[treePoolID(h, poolOf(path))] = true
	segs := strings.Split(path, "/")
	for i := range segs {
		m.expanded[treeDsID(h, strings.Join(segs[:i+1], "/"))] = true
	}
}
