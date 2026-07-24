package zfs

import (
	"math"
	"path/filepath"
	"testing"
)

func TestRaidzGeometry(t *testing.T) {
	// 8-wide raidz3, ashift 12 — the fixture pool's layout
	const w, p, a = 8, 3, 12

	if base := RaidzRawPerCharged(w, p, a); math.Abs(base-1.75) > 0.001 {
		t.Errorf("baseline = %.4f, want 1.75 (56 raw sectors per 32 data at 128K)", base)
	}
	cases := map[int64]float64{
		128 * 1024: 1.0,    // the baseline itself
		16 * 1024:  1.1428, // matches measured ×1.14 on vm-mars
		4 * 1024:   2.2857, // 1 data + 3 parity, padded to 4 -> 16K raw
	}
	for bs, want := range cases {
		if got := RaidzChargedInflation(bs, w, p, a); math.Abs(got-want) > 0.001 {
			t.Errorf("inflation(%d) = %.4f, want %.4f", bs, got, want)
		}
	}
}

func TestRaidzShape(t *testing.T) {
	cases := map[string]int{"raidz3-0": 3, "raidz1-4": 1, "raidz-0": 1, "raidz2-1": 2}
	for name, want := range cases {
		p, ok := RaidzShape(name)
		if !ok || p != want {
			t.Errorf("RaidzShape(%q) = %d,%v want %d", name, p, ok, want)
		}
	}
	if _, ok := RaidzShape("mirror-0"); ok {
		t.Error("mirror-0 misidentified as raidz")
	}
}

func TestParsePoolAshift(t *testing.T) {
	m := ParsePoolAshift(readFixture(t, filepath.Join(fixtureDir25, "zpool-ashift.out")))
	if len(m) != 3 || m["rust"] != 12 || m["bpool"] != 12 {
		t.Errorf("ashift map = %+v", m)
	}
}

func TestParseRootStats(t *testing.T) {
	m := ParseRootStats("rust\t100\t200\nbpool\t5\t-\n")
	if m["rust"].Used != 100 || m["rust"].LogicalUsed != 200 {
		t.Errorf("rust = %+v", m["rust"])
	}
	if m["bpool"].LogicalUsed != -1 {
		t.Errorf("bpool logical = %d, want -1", m["bpool"].LogicalUsed)
	}
}
