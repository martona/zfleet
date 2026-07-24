package ui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The tree screen: one expandable tree — overview row, pools, and as much
// of the dataset hierarchy as the user has unfolded. Two presentations of
// the same rows: cursor on "≡ overview" drops the inspector and renders
// full-width with io columns; cursor anywhere else is the classic two-pane
// browse. → expands in place, ← collapses (or jumps to parent), Enter
// drills into the deep-dive browser.

const overviewID = "≡"

const (
	rOverview = iota
	rPool
	rDataset
)

type treeRow struct {
	kind       int
	pool       *zfs.Pool
	ds         *zfs.Dataset
	depth      int // dataset rows: 1 = root dataset
	id         string
	parentID   string
	expandable bool
	expanded   bool
}

func treePoolID(name string) string { return "p:" + name }

func (m *Model) treeRows() []treeRow {
	rows := []treeRow{{kind: rOverview, id: overviewID}}
	for _, p := range m.pools {
		pid := treePoolID(p.Name)
		rows = append(rows, treeRow{
			kind: rPool, pool: p, id: pid,
			expandable: true, expanded: m.expanded[pid],
		})
		if !m.expanded[pid] {
			continue
		}
		tree := m.dsTrees[p.Name]
		if tree == nil {
			continue // fetch in flight; rows appear when it lands
		}
		root := tree.ByName[p.Name]
		if root == nil {
			continue
		}
		var walk func(d *zfs.Dataset, depth int, parent string)
		walk = func(d *zfs.Dataset, depth int, parent string) {
			exp := m.expanded[d.Name]
			rows = append(rows, treeRow{
				kind: rDataset, ds: d, depth: depth, id: d.Name, parentID: parent,
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
				walk(k, depth+1, d.Name)
			}
		}
		walk(root, 1, pid)
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
		m.setSel(rows[i].pool.Name) // resets the pool inspector's elastic widths
	}
}

// treeEnsure lazily fetches whatever the selected row's inspector needs.
func (m *Model) treeEnsure() tea.Cmd {
	row := m.treeSelected()
	if row.kind != rDataset {
		return nil
	}
	return tea.Batch(m.ensureSnapsCmd(row.ds.Name), m.ensurePropsCmd(row.ds.Name))
}

func (m *Model) treeKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "down", "j":
		m.treeMove(1)
	case "up", "k":
		m.treeMove(-1)
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
				return m, tea.Batch(m.ensureTreeCmd(row.pool.Name), m.treeEnsure())
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
		case rPool:
			return m, m.enterBrowserAt(row.pool.Name)
		case rDataset:
			return m, m.enterBrowserAt(row.ds.Name)
		}
	case "t":
		m.expandAll = !m.expandAll
	case "s":
		m.treeSortUsed = !m.treeSortUsed
	}
	return m, m.treeEnsure()
}

// ExpandFor unfolds the tree down to the given pool or dataset path
// (dump helper; ancestors expand too).
func (m *Model) ExpandFor(path string) {
	m.expanded[treePoolID(poolOf(path))] = true
	segs := strings.Split(path, "/")
	for i := range segs {
		m.expanded[strings.Join(segs[:i+1], "/")] = true
	}
}
