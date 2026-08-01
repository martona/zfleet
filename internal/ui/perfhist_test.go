package ui

import (
	"testing"
	"time"

	"github.com/martona/zfleet/internal/zfs"
)

// bankDirty buckets committed txgs into wall-clock windows by birth time:
// the chart scrolls at time speed, a txg storm collapses into few columns
// (peak-merged — thresholds stay honest), empty windows are -1 blanks, and
// the kernel ring's birth history backfills the axis on the first read.
func TestBankDirty(t *testing.T) {
	ring := []zfs.TxgRow{
		{Txg: 10, Birth: 1_000_000_000, State: "C", NDirty: 5},   // window 0
		{Txg: 11, Birth: 6_120_000_000, State: "C", NDirty: 0},   // window 3
		{Txg: 12, Birth: 11_240_000_000, State: "C", NDirty: 7},  // window 5
		{Txg: 13, Birth: 13_000_000_000, State: "O", NDirty: 99}, // open: not banked
	}
	win, lastTxg, lastIdx := bankDirty(nil, 0, 0, ring, perfDirtyWinCap)
	want := []int64{5, -1, -1, 0, -1, 7}
	if len(win) != len(want) || lastTxg != 12 || lastIdx != 5 {
		t.Fatalf("first bank = %v lastTxg=%d lastIdx=%d, want %v 12 5", win, lastTxg, lastIdx, want)
	}
	for i, v := range want {
		if win[i] != v {
			t.Fatalf("first bank = %v, want %v", win, want)
		}
	}

	// overlapping re-read: 13 committed since; older rows must not re-bank
	ring[3] = zfs.TxgRow{Txg: 13, Birth: 13_400_000_000, State: "C", NDirty: 3} // window 6
	win, lastTxg, lastIdx = bankDirty(win, lastTxg, lastIdx, ring, perfDirtyWinCap)
	if len(win) != 7 || win[6] != 3 || lastTxg != 13 {
		t.Fatalf("re-read bank = %v lastTxg=%d, want ...7,3 and 13", win, lastTxg)
	}

	// a storm inside one window peak-merges — never averages
	storm := []zfs.TxgRow{
		{Txg: 14, Birth: 13_500_000_000, State: "C", NDirty: 1},
		{Txg: 15, Birth: 13_600_000_000, State: "C", NDirty: 9},
		{Txg: 16, Birth: 13_700_000_000, State: "C", NDirty: 2},
	}
	win, lastTxg, lastIdx = bankDirty(win, lastTxg, lastIdx, storm, perfDirtyWinCap)
	if len(win) != 7 || win[6] != 9 {
		t.Fatalf("storm bank = %v, want single window peaked at 9", win)
	}

	// window 8 after a one-window gap, then cap trims to the last 4
	win, _, _ = bankDirty(win, lastTxg, lastIdx,
		[]zfs.TxgRow{{Txg: 17, Birth: 16_100_000_000, State: "C", NDirty: 4}}, 4)
	if len(win) != 4 || win[0] != 7 || win[1] != 9 || win[2] != -1 || win[3] != 4 {
		t.Fatalf("capped bank = %v, want [7 9 -1 4]", win)
	}
}

// A txg paints every window its life spans — the zeus lesson: a saturated
// pool cutting 35s txgs must render as a plateau, not one spike per txg
// and sync-time void.
func TestBankDirtySpan(t *testing.T) {
	// born at 1s, 3s open + 4s sync → alive through 8s: windows 0–4
	long := []zfs.TxgRow{{Txg: 20, Birth: 1_000_000_000, State: "C", NDirty: 8,
		OTime: 3_000_000_000, STime: 4_000_000_000}}
	win, lastTxg, lastIdx := bankDirty(nil, 0, 0, long, perfDirtyWinCap)
	if len(win) != 5 {
		t.Fatalf("span bank = %v, want 5 windows", win)
	}
	for i, v := range win {
		if v != 8 {
			t.Fatalf("span bank = %v, want 8 across the whole life (window %d)", win, i)
		}
	}

	// the pipelined successor: born at 5s while 20 still synced, commits
	// at 11s — overlap max-merges under the heavier txg, the tail extends
	next := []zfs.TxgRow{{Txg: 21, Birth: 5_000_000_000, State: "C", NDirty: 6,
		OTime: 4_000_000_000, STime: 2_000_000_000}}
	win, _, _ = bankDirty(win, lastTxg, lastIdx, next, perfDirtyWinCap)
	if len(win) != 6 || win[3] != 8 || win[4] != 8 || win[5] != 6 {
		t.Fatalf("overlap bank = %v, want [8 8 8 8 8 6]", win)
	}
}

// The live right edge: dirtyDisplay extends the banked window to now and
// paints in-flight txgs at the newest committed peak — the chart marches
// at wall speed instead of holding its breath for a 35s txg.
func TestDirtyDisplay(t *testing.T) {
	m := &Model{}
	ring := []zfs.TxgRow{
		{Txg: 30, Birth: 1_000_000_000, State: "C", NDirty: 5},
		{Txg: 31, Birth: 1_500_000_000, State: "O"}, // alive since 1.5s
	}
	m.perf.dirtyWin, m.perf.dirtyTxg, m.perf.dirtyIdx = bankDirty(nil, 0, 0, ring, perfDirtyWinCap)
	m.perf.txgs = ring
	m.perf.liveNS, m.perf.liveAt = 9_000_000_000, time.Now() // "now" ≈ 9s → window 4

	out := m.dirtyDisplay()
	if len(out) != 5 {
		t.Fatalf("display = %v, want 5 windows out to now", out)
	}
	for i, v := range out {
		if v != 5 {
			t.Fatalf("display = %v, want provisional 5 across the open txg's span (window %d)", out, i)
		}
	}
	if len(m.perf.dirtyWin) != 1 {
		t.Fatalf("banked window mutated by render: %v", m.perf.dirtyWin)
	}

	// nothing alive: the extension stays blank
	m.perf.txgs = ring[:1]
	out = m.dirtyDisplay()
	if len(out) != 5 || out[4] != -1 {
		t.Fatalf("display = %v, want blank tail with no txg in flight", out)
	}

	// idle: an alive txg with a zero-dirty last commit still dots the edge
	// (0 = baseline dot; -1 = blank — in-flight must read as calm, not absent)
	m.perf.txgs = []zfs.TxgRow{
		{Txg: 40, Birth: 1_000_000_000, State: "C", NDirty: 0},
		{Txg: 41, Birth: 7_000_000_000, State: "O"},
	}
	out = m.dirtyDisplay()
	if out[4] != 0 || out[3] != 0 {
		t.Fatalf("display = %v, want 0-dots from the alive txg's birth to now", out)
	}
}

// The zil line is the pool's own datasets' truth — the zeus lesson's other
// half: the host-global kstat pinned a rasdaemon fsync storm on rpool onto
// an innocent recv pool. poolZil must not leak across pools (nor onto
// prefix cousins) and must name the top committer.
func TestPoolZil(t *testing.T) {
	h := newHostState("h", "", nil)
	h.applyObjsets(map[string]zfs.ObjsetIO{
		"rust":       {ZilSeen: true},
		"rust/a":     {ZilSeen: true, ZilCommits: 100, ZilNormalB: 1000, ZilNormalC: 50},
		"rustic/x":   {ZilSeen: true, ZilCommits: 500},
		"rpool/root": {ZilSeen: true, ZilCommits: 10000},
	})
	h.objsetAt = time.Now().Add(-2 * time.Second) // age the sample: dt ≈ 2s
	h.applyObjsets(map[string]zfs.ObjsetIO{
		"rust":       {ZilSeen: true},
		"rust/a":     {ZilSeen: true, ZilCommits: 300, ZilNormalB: 3000, ZilNormalC: 60},
		"rustic/x":   {ZilSeen: true, ZilCommits: 700},
		"rpool/root": {ZilSeen: true, ZilCommits: 14000},
	})

	pz := h.poolZil("rust")
	if !pz.ok {
		t.Fatal("objset zil stats present but poolZil reports not-ok")
	}
	// rust/a: 200 commits over ~2s ≈ 100/s; rustic/* and rpool/* must not leak in
	if pz.commits < 80 || pz.commits > 120 {
		t.Fatalf("commits/s = %.1f, want ~100 (cross-pool or prefix leak?)", pz.commits)
	}
	if pz.topDS != "rust/a" {
		t.Fatalf("top committer = %q, want rust/a", pz.topDS)
	}
	if pz.normC != 60 {
		t.Fatalf("normC = %d, want 60 (rust's own itx only)", pz.normC)
	}
	if pz.normB < 800 || pz.normB > 1200 {
		t.Fatalf("normB = %d B/s, want ~1000", pz.normB)
	}

	// a host whose kmod lacks objset zil stats refuses: render falls back
	// to the global kstat with the honest "host-wide" caption
	h2 := newHostState("h2", "", nil)
	h2.applyObjsets(map[string]zfs.ObjsetIO{"p/a": {Writes: 1}})
	h2.objsetAt = time.Now().Add(-2 * time.Second)
	h2.applyObjsets(map[string]zfs.ObjsetIO{"p/a": {Writes: 2}})
	if h2.poolZil("p").ok {
		t.Fatal("poolZil claimed ok without objset zil stats")
	}
}
