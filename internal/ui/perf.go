package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The performance screen: a full-width dashboard for one pool's engine —
// ARC, txg pipeline, write throttle, per-vdev latency, ZIL — with a
// clearly-labeled heuristic reading. Reached with `p` from anywhere;
// `p`/Esc/Backspace returns to wherever you were. With remotes registered
// the bar grows a host line above the pool line: ↑/↓ move focus between
// the lines, ←/→ walk the focused one, and tab always means "next pool",
// wrapping across host boundaries. Its 2s fetch is armed only while the
// screen is open.

const modePerf = 2

const perfInterval = 2 * time.Second

type perfState struct {
	host *hostState
	pool string
	from int // mode to return to

	focusHosts bool // ↑/↓ focus: true = the host line owns ←/→

	txgs      []zfs.TxgRow // the kstat ring as last read (~100 rows)
	txgHist   []zfs.TxgRow // committed txgs banked across ticks for the chart
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

// The kernel's txg ring (zfs_txg_history) holds only ~100 rows; the dirty
// chart banks committed txgs across ticks to chart deeper than that.
const perfTxgAccum = 512

// mergeTxgs banks newly committed txgs from a ring read into the
// accumulated history. Dedupe is by txg number — the ring overlaps almost
// entirely between ticks; rows still open/quiescing get banked on a later
// read, once committed.
func mergeTxgs(hist, ring []zfs.TxgRow, cap int) []zfs.TxgRow {
	last := int64(-1)
	if len(hist) > 0 {
		last = hist[len(hist)-1].Txg
	}
	for _, r := range ring {
		if r.State == "C" && r.Txg > last {
			hist = append(hist, r)
		}
	}
	if len(hist) > cap {
		hist = hist[len(hist)-cap:]
	}
	return hist
}

type perfMsg struct {
	host   string
	pool   string
	txgs   string
	dmuTx  string
	zil    string
	params string
	iostat string
	err    error
}
type perfTickMsg struct{}

func fetchPerf(h *hostState, pool string) tea.Cmd {
	if h == nil {
		return nil
	}
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		txgs, dmuTx, zil, params, iostat, err := src.PerfTexts(ctx, pool)
		return perfMsg{host: host, pool: pool, txgs: txgs, dmuTx: dmuTx, zil: zil,
			params: params, iostat: iostat, err: err}
	}
}

// perfTarget resets the dashboard onto (host, pool), remembering the pool
// as that host's last-viewed.
func (m *Model) perfTarget(h *hostState, pool string, from int, focusHosts bool) tea.Cmd {
	m.perf = perfState{host: h, pool: pool, from: from, focusHosts: focusHosts}
	m.perfMem[h.name] = pool
	m.mode = modePerf
	return fetchPerf(h, pool)
}

// perfPoolFor picks the pool to land on for a host: the remembered one,
// else its first.
func (m *Model) perfPoolFor(h *hostState) string {
	if p := m.perfMem[h.name]; p != "" && h.pool(p) != nil {
		return p
	}
	if len(h.pools) > 0 {
		return h.pools[0].Name
	}
	return ""
}

// enterPerf opens the dashboard for the pool implied by the current
// context.
func (m *Model) enterPerf() tea.Cmd {
	var h *hostState
	pool := ""
	switch row := m.treeSelected(); row.kind {
	case rHost:
		h, pool = row.host, m.perfPoolFor(row.host)
	case rPool:
		h, pool = row.host, row.pool.Name
	case rDataset, rFam, rSnap:
		h, pool = row.host, poolOf(row.ds.Name)
	default:
		// overview: pick the busiest pool by current physical io,
		// fleet-wide
		var best int64 = -1
		for _, hh := range m.hosts {
			for _, p := range hh.pools {
				io := hh.io[p.Name]
				if v := io.RBw + io.WBw; v > best {
					best, h, pool = v, hh, p.Name
				}
			}
		}
	}
	if h == nil || pool == "" {
		return nil
	}
	from := m.mode
	return tea.Batch(m.perfTarget(h, pool, from, false),
		tea.Tick(perfInterval, func(time.Time) tea.Msg { return perfTickMsg{} }))
}

func (m *Model) exitPerf() {
	m.mode = m.perf.from
}

type perfPair struct {
	h    *hostState
	pool string
}

// perfPairs flattens the fleet into (host, pool) stops in display order.
func (m *Model) perfPairs() []perfPair {
	var out []perfPair
	for _, h := range m.hosts {
		for _, p := range h.pools {
			out = append(out, perfPair{h, p.Name})
		}
	}
	return out
}

// perfCycle moves to the next/previous pool, wrapping across host
// boundaries — today's muscle memory, fleet-sized.
func (m *Model) perfCycle(delta int) tea.Cmd {
	pairs := m.perfPairs()
	if len(pairs) == 0 {
		return nil
	}
	idx := 0
	for i, pr := range pairs {
		if pr.h == m.perf.host && pr.pool == m.perf.pool {
			idx = i
		}
	}
	idx = (idx + delta + len(pairs)) % len(pairs)
	return m.perfTarget(pairs[idx].h, pairs[idx].pool, m.perf.from, m.perf.focusHosts)
}

// perfCycleHost moves to the next/previous host, landing on its remembered
// pool. Hosts without pools are skipped — there is nothing to dashboard.
func (m *Model) perfCycleHost(delta int) tea.Cmd {
	var withPools []*hostState
	for _, h := range m.hosts {
		if len(h.pools) > 0 {
			withPools = append(withPools, h)
		}
	}
	if len(withPools) == 0 {
		return nil
	}
	idx := 0
	for i, h := range withPools {
		if h == m.perf.host {
			idx = i
		}
	}
	idx = (idx + delta + len(withPools)) % len(withPools)
	h := withPools[idx]
	return m.perfTarget(h, m.perfPoolFor(h), m.perf.from, m.perf.focusHosts)
}

func (m *Model) perfKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "p", "esc", "backspace":
		m.exitPerf()
	case "tab":
		return m, m.perfCycle(1)
	case "shift+tab":
		return m, m.perfCycle(-1)
	case "right", "l":
		if m.perf.focusHosts {
			return m, m.perfCycleHost(1)
		}
		return m, m.perfCycle(1)
	case "left", "h":
		if m.perf.focusHosts {
			return m, m.perfCycleHost(-1)
		}
		return m, m.perfCycle(-1)
	case "up", "down":
		if m.multiHost {
			m.perf.focusHosts = !m.perf.focusHosts
		}
	case "j":
		m.panelScroll++
	case "k":
		m.panelScroll--
	}
	return m, nil
}

func (m *Model) applyPerf(msg perfMsg) {
	if m.perf.host == nil || msg.host != m.perf.host.name || msg.pool != m.perf.pool {
		return
	}
	if msg.err != nil {
		m.perf.err = msg.err.Error()
	} else {
		m.perf.err = ""
	}
	m.perf.txgs = zfs.ParseTxgs(msg.txgs)
	m.perf.txgHist = mergeTxgs(m.perf.txgHist, m.perf.txgs, perfTxgAccum)
	m.perf.dmuPrev, m.perf.dmu = m.perf.dmu, zfs.ParseKstatMap(msg.dmuTx)
	m.perf.dmuPrevAt, m.perf.dmuAt = m.perf.dmuAt, time.Now()
	m.perf.zilPrev, m.perf.zil = m.perf.zil, zfs.ParseKstatMap(msg.zil)
	if p := zfs.ParseParams(msg.params); len(p) > 0 {
		m.perf.params = p
	}
	m.perf.lat = zfs.ParseIostatLatency(msg.iostat, m.perf.pool, m.perf.host.poolNames())
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

// latWindow reduces a device's ring to op-weighted column averages — each
// interval votes with its op count, so three slow ops in a quiet second
// can't outvote ten thousand fast ones (equal-interval averaging slanders
// sparse-burst drives). The worst single-sample write-total stays as the
// peak, un-averaged on purpose.
func (m *Model) latWindow(name string) (avg zfs.VdevLat, peakW int64, samples int) {
	ring := m.perf.latHist[name]
	if len(ring) == 0 {
		return zfs.VdevLat{TotalR: -1, TotalW: -1, DiskR: -1, DiskW: -1,
			SyncQR: -1, SyncQW: -1, AsyncQR: -1, AsyncQW: -1}, -1, 0
	}
	avg = zfs.OpWeightedLat(ring)
	peakW = -1
	for _, s := range ring {
		if s.TotalW > peakW {
			peakW = s.TotalW
		}
	}
	return avg, peakW, len(ring)
}

// EnterPerfFor opens the perf screen on a named pool of a host (dump helper).
func (m *Model) EnterPerfFor(host, pool string) bool {
	h := m.hostByName(host)
	if h == nil || h.pool(pool) == nil {
		return false
	}
	m.perf = perfState{host: h, pool: pool, from: m.mode}
	m.perfMem[h.name] = pool
	m.mode = modePerf
	return true
}

// ApplyPerf ingests raw perf texts outside the tea loop (dump helper).
func (m *Model) ApplyPerf(host, pool, txgs, dmuTx, zil, params, iostat string, err error) {
	m.applyPerf(perfMsg{host: host, pool: pool, txgs: txgs, dmuTx: dmuTx, zil: zil,
		params: params, iostat: iostat, err: err})
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
