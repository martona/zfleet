package ui

import (
	"testing"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// mergeTxgs banks committed txgs across overlapping ring reads — the kernel
// remembers ~100, the dirty chart wants more.
func TestMergeTxgs(t *testing.T) {
	ring1 := []zfs.TxgRow{
		{Txg: 10, State: "C", NDirty: 1},
		{Txg: 11, State: "C", NDirty: 2},
		{Txg: 12, State: "O"}, // open: not banked yet
	}
	hist := mergeTxgs(nil, ring1, 4)
	if len(hist) != 2 || hist[1].Txg != 11 {
		t.Fatalf("first merge = %+v, want txgs 10,11", hist)
	}
	// overlapping re-read: 12 has committed, 13 is the new open head
	ring2 := []zfs.TxgRow{
		{Txg: 10, State: "C", NDirty: 1},
		{Txg: 11, State: "C", NDirty: 2},
		{Txg: 12, State: "C", NDirty: 3},
		{Txg: 13, State: "O"},
	}
	hist = mergeTxgs(hist, ring2, 4)
	if len(hist) != 3 || hist[2].Txg != 12 {
		t.Fatalf("second merge = %+v, want txgs 10,11,12 (no dupes)", hist)
	}
	// cap trims the oldest
	ring3 := []zfs.TxgRow{
		{Txg: 13, State: "C", NDirty: 4},
		{Txg: 14, State: "C", NDirty: 5},
	}
	hist = mergeTxgs(hist, ring3, 4)
	if len(hist) != 4 || hist[0].Txg != 11 || hist[3].Txg != 14 {
		t.Fatalf("capped merge = %+v, want txgs 11..14", hist)
	}
}
