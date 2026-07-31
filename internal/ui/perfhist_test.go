package ui

import (
	"testing"

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
