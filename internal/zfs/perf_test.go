package zfs

import (
	"path/filepath"
	"testing"
)

func TestParseTxgs(t *testing.T) {
	rows := ParseTxgs(readFixture(t, filepath.Join(fixtureDir25, "txgs-rust.out")))
	if len(rows) < 50 {
		t.Fatalf("txg rows = %d, want ~100", len(rows))
	}
	s := SummarizeTxgs(rows, 20)
	if s.Committed == 0 {
		t.Fatal("no committed txgs")
	}
	// idle pool heartbeat: ~5s open time, ~12 txg/min
	if s.OAvg < 4e9 || s.OAvg > 7e9 {
		t.Errorf("otime avg = %d ns, want ~5s", s.OAvg)
	}
	if s.PerMinute < 8 || s.PerMinute > 16 {
		t.Errorf("txg/min = %.1f, want ~12", s.PerMinute)
	}
}

func TestParseKstatMapAndParams(t *testing.T) {
	dmu := ParseKstatMap(readFixture(t, filepath.Join(fixtureDir25, "dmu-tx.out")))
	if dmu["dmu_tx_dirty_delay"] != 161 {
		t.Errorf("dirty_delay = %d, want 161", dmu["dmu_tx_dirty_delay"])
	}
	zil := ParseKstatMap(readFixture(t, filepath.Join(fixtureDir25, "zil.out")))
	if zil["zil_commit_count"] != 12412 || zil["zil_itx_metaslab_slog_count"] != 0 {
		t.Errorf("zil = %d/%d", zil["zil_commit_count"], zil["zil_itx_metaslab_slog_count"])
	}
	p := ParseParams(readFixture(t, filepath.Join(fixtureDir25, "zfs-params.out")))
	if p["zfs_dirty_data_max"] != 4294967296 || p["zfs_delay_min_dirty_percent"] != 60 {
		t.Errorf("params = %+v", p)
	}
}

func TestParseIostatLatency(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir25, "zpool-iostat-Hpvly.out"))
	names := map[string]bool{"bpool": true, "rpool": true, "rust": true}
	lat := ParseIostatLatency(text, "rust", names)
	if _, ok := lat["raidz3-0"]; !ok {
		t.Fatalf("raidz3-0 missing: have %d rows", len(lat))
	}
	if _, ok := lat["mirror-0"]; ok {
		t.Error("mirror-0 leaked from another pool's section")
	}
	// idle interval: dashes parse to -1
	if v := lat["raidz3-0"]; v.TotalW != -1 && v.TotalW < 0 {
		t.Errorf("raidz3-0 = %+v", v)
	}
}

func TestOpWeightedLat(t *testing.T) {
	samples := []VdevLat{
		{WOps: 3, TotalW: 30e6},    // sparse burst: 3 ops at 30ms
		{WOps: 10000, TotalW: 4e5}, // steady: 10k ops at 400µs
		{WOps: 0, TotalW: -1},      // idle interval: must not count
		{ROps: 0, TotalR: -1, WOps: 0, TotalW: -1},
	}
	got := OpWeightedLat(samples)
	// op-weighted ≈ 409µs; equal-interval would claim ~15ms
	if got.TotalW < 4e5 || got.TotalW > 5e5 {
		t.Errorf("TotalW = %d ns, want ~409µs", got.TotalW)
	}
	if got.TotalR != -1 {
		t.Errorf("TotalR = %d, want -1 (no read ops anywhere)", got.TotalR)
	}
	if got.WOps != 10003 {
		t.Errorf("WOps sum = %d, want 10003", got.WOps)
	}
}

func TestNiceNS(t *testing.T) {
	cases := map[int64]string{
		-1: "-", 850: "850ns", 12000: "12.0µs", 1700000: "1.70ms",
		5119908783: "5.12s",
	}
	for in, want := range cases {
		if got := NiceNS(in); got != want {
			t.Errorf("NiceNS(%d) = %q, want %q", in, got, want)
		}
	}
}
