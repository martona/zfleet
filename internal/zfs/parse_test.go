package zfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureDir = "../../testdata/fixtures/commodoreplus4/2026-07-24"

func readFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// testdata/ is gitignored (real fixtures carry hostnames and
			// disk serials); clones without it skip rather than fail
			t.Skipf("fixture not present in this checkout: %s", path)
		}
		t.Fatalf("fixture: %v", err)
	}
	return string(b)
}

func loadPools(t *testing.T) []*Pool {
	t.Helper()
	pools := ParseZpoolStatus(readFixture(t, filepath.Join(fixtureDir, "zpool-status.out")))
	AttachListNumbers(pools, readFixture(t, filepath.Join(fixtureDir, "zpool-list-Hpv.out")))
	return pools
}

func TestParseStatusRealFixture(t *testing.T) {
	pools := loadPools(t)
	if len(pools) != 3 {
		t.Fatalf("pools = %d, want 3", len(pools))
	}
	for _, p := range pools {
		if p.State != "ONLINE" {
			t.Errorf("%s state = %q", p.Name, p.State)
		}
		if !p.Healthy() {
			t.Errorf("%s not healthy", p.Name)
		}
		if p.Scan.State != ScanDone {
			t.Errorf("%s scan state = %v, want done", p.Name, p.Scan.State)
		}
		if p.ErrorsLine != "No known data errors" {
			t.Errorf("%s errors = %q", p.Name, p.ErrorsLine)
		}
	}

	rust := pools[2]
	if rust.Name != "rust" {
		t.Fatalf("pool[2] = %s", rust.Name)
	}
	wantClasses := []struct {
		name   string
		vdevs  int
		leaves int
	}{{"data", 2, 16}, {"special", 1, 2}, {"logs", 1, 2}}
	if len(rust.Classes) != len(wantClasses) {
		t.Fatalf("rust classes = %d, want %d", len(rust.Classes), len(wantClasses))
	}
	for i, w := range wantClasses {
		c := rust.Classes[i]
		if c.Name != w.name || len(c.Vdevs) != w.vdevs {
			t.Errorf("class %d = %s/%d vdevs, want %s/%d", i, c.Name, len(c.Vdevs), w.name, w.vdevs)
		}
		leaves := 0
		for _, v := range c.Vdevs {
			leaves += len(v.Leaves())
		}
		if leaves != w.leaves {
			t.Errorf("class %s leaves = %d, want %d", c.Name, leaves, w.leaves)
		}
	}
	if got := rust.Scan.Summary; got != "ok Jul 12 · 11h12m · repaired 0B" {
		t.Errorf("rust scan summary = %q", got)
	}
}

func TestAttachListNumbers(t *testing.T) {
	pools := loadPools(t)
	rust := pools[2]
	if rust.Size != 320490459627520 || rust.Alloc != 103545312116736 {
		t.Errorf("rust size/alloc = %d/%d", rust.Size, rust.Alloc)
	}
	if rust.CapPct != 32 || rust.FragPct != 0 {
		t.Errorf("rust cap/frag = %d/%d", rust.CapPct, rust.FragPct)
	}
	sp := rust.Class("special")
	if sp == nil || sp.Size != 498216206336 || sp.Alloc != 49814470656 {
		t.Fatalf("special class size/alloc wrong: %+v", sp)
	}
	data := rust.Class("data")
	if data == nil || data.Size != 2*159996121710592 {
		t.Fatalf("data class size wrong: %+v", data)
	}
	if pools[0].CapPct != 43 {
		t.Errorf("bpool cap = %d, want 43", pools[0].CapPct)
	}
	// leaf disks get size but no alloc/free in -Hpv
	disk := data.Vdevs[0].Children[0]
	if disk.Size != 20000578469888 || disk.Alloc != -1 {
		t.Errorf("leaf disk size/alloc = %d/%d", disk.Size, disk.Alloc)
	}
}

func TestParseStatusDegradedSynthetic(t *testing.T) {
	text := readFixture(t, "../../testdata/fixtures/synthetic/degraded-pool/zpool-status.out")
	pools := ParseZpoolStatus(text)
	if len(pools) != 1 {
		t.Fatalf("pools = %d", len(pools))
	}
	p := pools[0]
	if p.State != "DEGRADED" || p.Healthy() {
		t.Errorf("state = %q healthy = %v", p.State, p.Healthy())
	}
	if len(p.Notes) != 3 {
		t.Errorf("notes = %d, want 3 (status/action/see, continuations joined)", len(p.Notes))
	}
	if !strings.HasSuffix(p.Notes[0], "functioning in a degraded state.") {
		t.Errorf("status note not joined across wraps: %q", p.Notes[0])
	}
	if p.Scan.State != ScanInProgress || p.Scan.Kind != "resilver" {
		t.Errorf("scan = %+v", p.Scan)
	}
	if p.Scan.Percent < 28.10 || p.Scan.Percent > 28.12 {
		t.Errorf("scan pct = %v", p.Scan.Percent)
	}
	if p.Scan.Summary != "28.1% done · ~2h31m to go" {
		t.Errorf("scan summary = %q", p.Scan.Summary)
	}

	data := p.Class("data")
	if data == nil || len(data.Vdevs) != 1 {
		t.Fatalf("data class: %+v", data)
	}
	m0 := data.Vdevs[0]
	if m0.Name != "mirror-0" || m0.State != "DEGRADED" || len(m0.Children) != 2 {
		t.Fatalf("mirror-0: %+v", m0)
	}
	repl := m0.Children[1]
	if repl.Name != "replacing-1" || len(repl.Children) != 2 {
		t.Fatalf("replacing-1: %+v", repl)
	}
	bad := repl.Children[0]
	if bad.State != "UNAVAIL" || bad.ReadErr != "3" || bad.WriteErr != "12" || bad.Note != "cannot open" {
		t.Errorf("bad disk: %+v", bad)
	}
	if logs := p.Class("logs"); logs == nil || len(logs.Vdevs) != 1 || len(logs.Vdevs[0].Children) != 2 {
		t.Errorf("logs class: %+v", logs)
	}
}

func TestParseIostatPools(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir, "zpool-iostat-Hpv.out"))
	io := ParseIostatPools(text, map[string]bool{"bpool": true, "rpool": true, "rust": true})
	if len(io) != 3 {
		t.Fatalf("io pools = %d", len(io))
	}
	// two samples in the fixture; the later (true-interval, idle) must win
	for name, r := range io {
		if r.RBw != 0 || r.WBw != 0 {
			t.Errorf("%s rates = %+v, want zeros from second sample", name, r)
		}
	}
}

func TestParseArcstats(t *testing.T) {
	a := ParseArcstats(readFixture(t, filepath.Join(fixtureDir, "arcstats.out")))
	if a.Size != 5071757672 || a.CMax != 133951893504 {
		t.Errorf("arc size/cmax = %d/%d", a.Size, a.CMax)
	}
	if a.Hits != 71613748 || a.Misses != 146924 {
		t.Errorf("arc hits/misses = %d/%d", a.Hits, a.Misses)
	}
}

func TestNiceBytes(t *testing.T) {
	cases := map[int64]string{
		0:               "0B",
		2013265920:      "1.88G",
		2147483648:      "2G",
		49814470656:     "46.4G",
		103545312116736: "94.2T",
		320490459627520: "291T",
		133951893504:    "125G",
		-1:              "-",
	}
	for in, want := range cases {
		if got := NiceBytes(in); got != want {
			t.Errorf("NiceBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestNiceClockDur(t *testing.T) {
	cases := map[string]string{
		"11:12:02":        "11h12m",
		"00:00:32":        "0m32s",
		"2 days 03:04:05": "2d3h",
	}
	for in, want := range cases {
		if got := NiceClockDur(in); got != want {
			t.Errorf("NiceClockDur(%q) = %q, want %q", in, got, want)
		}
	}
}
