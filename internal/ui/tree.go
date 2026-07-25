package ui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The tree screen: one expandable tree — overview row, hosts (when any
// remote is registered), pools, and as much of the dataset hierarchy as the
// user has unfolded. Two presentations of the same rows: cursor on
// "≡ overview" drops the inspector and renders full-width with io columns;
// cursor anywhere else is the classic two-pane browse. → expands in place,
// ← collapses (or jumps to parent), Enter drills into the deep-dive
// browser. Host rows are organizational anchors: always open, never
// collapsible — folding one would hide the very info you came looking for.

const overviewID = "≡"

const (
	rOverview = iota
	rHost
	rPool
	rDataset
)

type treeRow struct {
	kind       int
	host       *hostState
	pool       *zfs.Pool
	ds         *zfs.Dataset
	depth      int // dataset rows: 1 = root dataset
	id         string
	parentID   string
	expandable bool
	expanded   bool
}

// row ids are host-qualified so identical pool names on different hosts
// stay distinct; \x00 can appear in neither
func treeHostID(h *hostState) string              { return "h:" + h.name }
func treePoolID(h *hostState, name string) string { return "p:" + h.name + "\x00" + name }
func treeDsID(h *hostState, name string) string   { return h.name + "\x00" + name }

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

func (m *Model) treeRows() []treeRow {
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
				continue // fetch in flight; rows appear when it lands
			}
			root := tree.ByName[p.Name]
			if root == nil {
				continue
			}
			var walk func(d *zfs.Dataset, depth int, parent string)
			walk = func(d *zfs.Dataset, depth int, parent string) {
				id := treeDsID(h, d.Name)
				exp := m.expanded[id]
				rows = append(rows, treeRow{
					kind: rDataset, host: h, ds: d, depth: depth, id: id, parentID: parent,
					expandable: len(d.Children) > 0, expanded: exp,
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
					walk(k, depth+1, id)
				}
			}
			walk(root, 1, pid)
		}
	}
	return rows
}

func (m *Model) treeIdx(rows []treeRow) int {
	for i, r := range rows {
		if r.id == m.treeSel {
			return i
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
	i := m.treeIdx(rows) + delta
	if i < 0 {
		i = 0
	}
	if i > len(rows)-1 {
		i = len(rows) - 1
	}
	m.treeSel = rows[i].id
	if rows[i].kind == rPool {
		m.setSel(rows[i].host, rows[i].pool.Name) // resets the pool inspector's elastic widths
	}
}

// treeEnsure lazily fetches whatever the selected row's inspector needs.
func (m *Model) treeEnsure() tea.Cmd {
	row := m.treeSelected()
	if row.kind != rDataset {
		return nil
	}
	return tea.Batch(m.ensureSnapsCmd(row.host, row.ds.Name),
		m.ensurePropsCmd(row.host, row.ds.Name))
}

func (m *Model) treeKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "down":
		m.treeMove(1)
	case "up":
		m.treeMove(-1)
	case "j":
		m.panelScroll++
	case "k":
		m.panelScroll--
	case "g":
		m.treeSel = overviewID
	case "G":
		if rows := m.treeRows(); len(rows) > 0 {
			m.treeSel = rows[len(rows)-1].id
		}
	case "right", "l":
		row := m.treeSelected()
		if row.expandable && !row.expanded {
			m.expanded[row.id] = true
			if row.kind == rPool {
				return m, tea.Batch(m.ensureTreeCmd(row.host, row.pool.Name), m.treeEnsure())
			}
		}
	case "left", "h":
		row := m.treeSelected()
		switch {
		case row.expanded:
			delete(m.expanded, row.id)
		case row.parentID != "":
			m.treeSel = row.parentID
		}
	case "enter":
		switch row := m.treeSelected(); row.kind {
		case rHost:
			return m, m.enterBrowserAt(row.host, "")
		case rPool:
			return m, m.enterBrowserAt(row.host, row.pool.Name)
		case rDataset:
			return m, m.enterBrowserAt(row.host, row.ds.Name)
		}
	case "s":
		m.treeSortUsed = !m.treeSortUsed
	}
	return m, m.treeEnsure()
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
