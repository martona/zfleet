package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/zfs"
)

const (
	poolsInterval = 5 * time.Second
	statsInterval = 2 * time.Second
)

type Model struct {
	src collect.Source

	w, h int

	pools   []*zfs.Pool
	arc     zfs.ArcStats
	arcPrev zfs.ArcStats
	haveArc bool
	io      map[string]zfs.IORates
	ioText  string

	selName   string
	expandAll bool
	lastErr   error

	// grow-only readout widths; ioW is per-pool (reset on selection change),
	// stripW lives for the session
	ioW    map[string]int
	stripW map[string]int
}

func New(src collect.Source) *Model {
	return &Model{
		src:    src,
		io:     map[string]zfs.IORates{},
		ioW:    map[string]int{},
		stripW: map[string]int{},
	}
}

func (m *Model) setSel(name string) {
	if name != m.selName {
		m.selName = name
		m.ioW = map[string]int{}
	}
}

type poolsDataMsg struct {
	pools []*zfs.Pool
	err   error
}
type statsDataMsg struct {
	arcText string
	ioText  string
	err     error
}
type poolsTickMsg struct{}
type statsTickMsg struct{}

func fetchPools(src collect.Source) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, list, err := src.PoolTexts(ctx)
		if err != nil {
			return poolsDataMsg{err: err}
		}
		pools := zfs.ParseZpoolStatus(status)
		zfs.AttachListNumbers(pools, list)
		return poolsDataMsg{pools: pools}
	}
}

func fetchStats(src collect.Source) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		arc, iostat, err := src.StatTexts(ctx)
		return statsDataMsg{arcText: arc, ioText: iostat, err: err}
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(fetchPools(m.src), fetchStats(m.src))
}

func (m *Model) poolNames() map[string]bool {
	names := map[string]bool{}
	for _, p := range m.pools {
		names[p.Name] = true
	}
	return names
}

func (m *Model) selIdx() int {
	for i, p := range m.pools {
		if p.Name == m.selName {
			return i
		}
	}
	return 0
}

// ApplyPoolData ingests parsed pools outside the tea loop (used by --dump).
func (m *Model) ApplyPoolData(pools []*zfs.Pool) {
	m.pools = pools
	if m.selName == "" && len(pools) > 0 {
		// land on the sickest pool; ties go to the first
		best := 0
		for i, p := range pools {
			if zfs.StateRank(p.State) > zfs.StateRank(pools[best].State) {
				best = i
			}
		}
		m.selName = pools[best].Name
	}
	if m.ioText != "" {
		m.io = zfs.ParseIostatPools(m.ioText, m.poolNames())
	}
}

// ApplyStatData ingests raw stat text outside the tea loop (used by --dump).
func (m *Model) ApplyStatData(arcText, ioText string) {
	if strings.TrimSpace(arcText) != "" {
		m.arcPrev = m.arc
		m.arc = zfs.ParseArcstats(arcText)
		m.haveArc = true
	}
	m.ioText = ioText
	if len(m.pools) > 0 {
		m.io = zfs.ParseIostatPools(ioText, m.poolNames())
	}
}

// SetSize sets the frame size directly (used by --dump).
func (m *Model) SetSize(w, h int) { m.w, m.h = w, h }

// SetSelected moves the cursor to the named pool if it exists (used by
// --dump --select).
func (m *Model) SetSelected(name string) {
	for _, p := range m.pools {
		if p.Name == name {
			m.setSel(name)
		}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case poolsDataMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.ApplyPoolData(msg.pools)
		}
		return m, tea.Tick(poolsInterval, func(time.Time) tea.Msg { return poolsTickMsg{} })

	case statsDataMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.ApplyStatData(msg.arcText, msg.ioText)
		}
		return m, tea.Tick(statsInterval, func(time.Time) tea.Msg { return statsTickMsg{} })

	case poolsTickMsg:
		return m, fetchPools(m.src)

	case statsTickMsg:
		return m, fetchStats(m.src)

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "down", "j":
			if i := m.selIdx(); i < len(m.pools)-1 {
				m.setSel(m.pools[i+1].Name)
			}
		case "up", "k":
			if i := m.selIdx(); i > 0 {
				m.setSel(m.pools[i-1].Name)
			}
		case "g":
			if len(m.pools) > 0 {
				m.setSel(m.pools[0].Name)
			}
		case "G":
			if len(m.pools) > 0 {
				m.setSel(m.pools[len(m.pools)-1].Name)
			}
		case "t":
			m.expandAll = !m.expandAll
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) View() string {
	return frame(m)
}
