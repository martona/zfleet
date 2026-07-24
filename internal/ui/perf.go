package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/zfs"
)

// The performance screen: a full-width dashboard for one pool's engine —
// ARC, txg pipeline, write throttle, per-vdev latency, ZIL — with a
// clearly-labeled heuristic reading. Reached with `p` from anywhere;
// `p`/Esc/Backspace returns to wherever you were. Its 2s fetch is armed
// only while the screen is open.

const modePerf = 2

const perfInterval = 2 * time.Second

type perfState struct {
	pool string
	from int // mode to return to

	txgs      []zfs.TxgRow
	dmu       map[string]int64
	dmuPrev   map[string]int64
	dmuAt     time.Time
	dmuPrevAt time.Time
	zil       map[string]int64
	zilPrev   map[string]int64
	params    map[string]int64
	lat       map[string]zfs.VdevLat
	latHist   map[string][]zfs.VdevLat // per-device ring; windowed view
	err       string
	have      bool
}

// ~60s of samples at the 2s tick — enough to separate a persistent
// straggler from rotating GC noise.
const perfLatHistLen = 30

type perfMsg struct {
	pool   string
	txgs   string
	dmuTx  string
	zil    string
	params string
	iostat string
	err    error
}
type perfTickMsg struct{}

func fetchPerf(src collect.Source, pool string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		txgs, dmuTx, zil, params, iostat, err := src.PerfTexts(ctx, pool)
		return perfMsg{pool: pool, txgs: txgs, dmuTx: dmuTx, zil: zil,
			params: params, iostat: iostat, err: err}
	}
}

// enterPerf opens the dashboard for the pool implied by the current
// context.
func (m *Model) enterPerf() tea.Cmd {
	pool := ""
	switch m.mode {
	case modeBrowser:
		pool = m.br.pool
	default:
		switch row := m.treeSelected(); row.kind {
		case rPool:
			pool = row.pool.Name
		case rDataset:
			pool = poolOf(row.ds.Name)
		default:
			// overview: pick the busiest pool by current physical io
			var best int64 = -1
			for _, p := range m.pools {
				io := m.io[p.Name]
				if v := io.RBw + io.WBw; v > best {
					best, pool = v, p.Name
				}
			}
		}
	}
	if pool == "" {
		return nil
	}
	m.perf = perfState{pool: pool, from: m.mode}
	m.mode = modePerf
	return tea.Batch(fetchPerf(m.src, pool),
		tea.Tick(perfInterval, func(time.Time) tea.Msg { return perfTickMsg{} }))
}

func (m *Model) exitPerf() {
	m.mode = m.perf.from
}

// perfCycle moves to the next/previous pool, keeping deltas fresh by
// resetting per-pool sample state.
func (m *Model) perfCycle(delta int) tea.Cmd {
	if len(m.pools) == 0 {
		return nil
	}
	idx := 0
	for i, p := range m.pools {
		if p.Name == m.perf.pool {
			idx = i
		}
	}
	idx = (idx + delta + len(m.pools)) % len(m.pools)
	from := m.perf.from
	m.perf = perfState{pool: m.pools[idx].Name, from: from}
	return fetchPerf(m.src, m.perf.pool)
}

func (m *Model) perfKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "p", "esc", "backspace":
		m.exitPerf()
	case "tab", "right", "l":
		return m, m.perfCycle(1)
	case "shift+tab", "left", "h":
		return m, m.perfCycle(-1)
	case "t":
		m.expandAll = !m.expandAll // force leaf disks in the latency table
	}
	return m, nil
}

func (m *Model) applyPerf(msg perfMsg) {
	if msg.pool != m.perf.pool {
		return
	}
	if msg.err != nil {
		m.perf.err = msg.err.Error()
	} else {
		m.perf.err = ""
	}
	m.perf.txgs = zfs.ParseTxgs(msg.txgs)
	m.perf.dmuPrev, m.perf.dmu = m.perf.dmu, zfs.ParseKstatMap(msg.dmuTx)
	m.perf.dmuPrevAt, m.perf.dmuAt = m.perf.dmuAt, time.Now()
	m.perf.zilPrev, m.perf.zil = m.perf.zil, zfs.ParseKstatMap(msg.zil)
	if p := zfs.ParseParams(msg.params); len(p) > 0 {
		m.perf.params = p
	}
	m.perf.lat = zfs.ParseIostatLatency(msg.iostat, m.perf.pool, m.poolNames())
	if m.perf.latHist == nil {
		m.perf.latHist = map[string][]zfs.VdevLat{}
	}
	for name, l := range m.perf.lat {
		ring := append(m.perf.latHist[name], l)
		if len(ring) > perfLatHistLen {
			ring = ring[1:]
		}
		m.perf.latHist[name] = ring
	}
	m.perf.have = true
}

// latWindow averages a device's ring per column (idle "-" samples excluded)
// and returns the worst single-sample write-total as the peak.
func (m *Model) latWindow(name string) (avg zfs.VdevLat, peakW int64, samples int) {
	ring := m.perf.latHist[name]
	none := zfs.VdevLat{TotalR: -1, TotalW: -1, DiskR: -1, DiskW: -1,
		SyncQR: -1, SyncQW: -1, AsyncQR: -1, AsyncQW: -1}
	if len(ring) == 0 {
		return none, -1, 0
	}
	mean := func(get func(zfs.VdevLat) int64) int64 {
		var sum int64
		n := 0
		for _, s := range ring {
			if v := get(s); v >= 0 {
				sum += v
				n++
			}
		}
		if n == 0 {
			return -1
		}
		return sum / int64(n)
	}
	avg = zfs.VdevLat{
		TotalR:  mean(func(s zfs.VdevLat) int64 { return s.TotalR }),
		TotalW:  mean(func(s zfs.VdevLat) int64 { return s.TotalW }),
		DiskR:   mean(func(s zfs.VdevLat) int64 { return s.DiskR }),
		DiskW:   mean(func(s zfs.VdevLat) int64 { return s.DiskW }),
		SyncQR:  mean(func(s zfs.VdevLat) int64 { return s.SyncQR }),
		SyncQW:  mean(func(s zfs.VdevLat) int64 { return s.SyncQW }),
		AsyncQR: mean(func(s zfs.VdevLat) int64 { return s.AsyncQR }),
		AsyncQW: mean(func(s zfs.VdevLat) int64 { return s.AsyncQW }),
	}
	peakW = -1
	for _, s := range ring {
		if s.TotalW > peakW {
			peakW = s.TotalW
		}
	}
	return avg, peakW, len(ring)
}

// EnterPerfFor opens the perf screen on a named pool (dump helper).
func (m *Model) EnterPerfFor(pool string) bool {
	for _, p := range m.pools {
		if p.Name == pool {
			m.perf = perfState{pool: pool, from: m.mode}
			m.mode = modePerf
			return true
		}
	}
	return false
}

// ApplyPerf ingests raw perf texts outside the tea loop (dump helper).
func (m *Model) ApplyPerf(pool, txgs, dmuTx, zil, params, iostat string, err error) {
	m.applyPerf(perfMsg{pool: pool, txgs: txgs, dmuTx: dmuTx, zil: zil,
		params: params, iostat: iostat, err: err})
}

// arcRate computes a per-second delta for an arcstats counter.
func (m *Model) arcRate(key string) float64 {
	if m.arcMapPrev == nil {
		return 0
	}
	dt := m.arcAt.Sub(m.arcPrevAt).Seconds()
	if dt <= 0 {
		return 0
	}
	d := m.arcMap[key] - m.arcMapPrev[key]
	if d < 0 {
		return 0
	}
	return float64(d) / dt
}

// perfRate computes a per-second delta for a counter across the last two
// samples.
func (m *Model) perfRate(cur, prev map[string]int64, key string) float64 {
	if prev == nil {
		return 0
	}
	dt := m.perf.dmuAt.Sub(m.perf.dmuPrevAt).Seconds()
	if dt <= 0 {
		return 0
	}
	d := cur[key] - prev[key]
	if d < 0 {
		return 0
	}
	return float64(d) / dt
}
