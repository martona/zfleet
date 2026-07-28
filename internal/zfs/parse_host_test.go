package zfs

import (
	"path/filepath"
	"testing"
)

const fixtureDirMH = "../../testdata/fixtures/commodoreplus4/2026-07-24-mh"

func TestParseUptime(t *testing.T) {
	if got := ParseUptime(readFixture(t, filepath.Join(fixtureDirMH, "uptime.out"))); got != 340116 {
		t.Errorf("uptime = %d, want 340116", got)
	}
	if ParseUptime("") != 0 || ParseUptime("junk here") != 0 {
		t.Error("garbage should parse to 0")
	}
}

func TestParseLoad1(t *testing.T) {
	if got := ParseLoad1(readFixture(t, filepath.Join(fixtureDirMH, "loadavg.out"))); got != "0.07" {
		t.Errorf("load1 = %q, want 0.07", got)
	}
	if ParseLoad1("") != "" {
		t.Error("empty should stay empty")
	}
}

func TestParseCPUStat(t *testing.T) {
	busy, total := ParseCPUStat(readFixture(t, filepath.Join(fixtureDirMH, "proc-stat.out")))
	// cpu 385657 7918 838815 1358482849 250532 0 61162 0
	wantBusy := int64(385657 + 7918 + 838815 + 61162)
	wantTotal := wantBusy + 1358482849 + 250532
	if busy != wantBusy || total != wantTotal {
		t.Errorf("cpu stat = (%d, %d), want (%d, %d)", busy, total, wantBusy, wantTotal)
	}
	if b, tot := ParseCPUStat("cpu0 1 2 3 4 5 6 7 8"); b != 0 || tot != 0 {
		t.Error("per-core lines must not match the aggregate")
	}
}

func TestParseHwmon(t *testing.T) {
	chips := ParseHwmon(readFixture(t, filepath.Join(fixtureDirMH, "hwmon.out")))
	// enp9s0 (NIC) + two coretemp packages; since the protan round every
	// chip is surfaced — the NIC is a named row now, not a silent loser
	if len(chips) != 3 {
		t.Fatalf("chips = %d, want 3", len(chips))
	}
	if chips[0].Name != "enp9s0" || chips[0].MaxC() != 64 {
		t.Errorf("chip0 = %s %d°C, want enp9s0 64°C", chips[0].Name, chips[0].MaxC())
	}
	if chips[2].Name != "coretemp" || chips[2].MaxC() != 45 {
		t.Errorf("chip2 = %s %d°C, want coretemp 45°C", chips[2].Name, chips[2].MaxC())
	}
	// labels pair with their inputs: temp1 of coretemp.1 is Package id 1
	if chips[2].Temps[0].Label != "Package id 1" {
		t.Errorf("label = %q, want Package id 1", chips[2].Temps[0].Label)
	}
	if got := ParseHwmon(""); len(got) != 0 {
		t.Error("empty input must yield no chips")
	}
}

func TestParseHwmonThresholds(t *testing.T) {
	text := "/sys/class/hwmon/hwmon0/name:k10temp\n" +
		"/sys/class/hwmon/hwmon0/temp1_input:61000\n" +
		"/sys/class/hwmon/hwmon0/temp1_max:60000\n" +
		"/sys/class/hwmon/hwmon0/temp1_crit:70000\n" +
		"/sys/class/hwmon/hwmon1/name:junk\n" +
		"/sys/class/hwmon/hwmon1/temp1_input:30000\n" +
		"/sys/class/hwmon/hwmon1/temp1_max:0\n" + // zero: never trusted
		"/sys/class/hwmon/hwmon1/temp2_input:31000\n" +
		"/sys/class/hwmon/hwmon1/temp2_max:50000\n" +
		"/sys/class/hwmon/hwmon1/temp2_crit:40000\n" // crit ≤ max: dropped
	chips := ParseHwmon(text)
	if len(chips) != 2 {
		t.Fatalf("chips = %d, want 2", len(chips))
	}
	if t0 := chips[0].Temps[0]; t0.MaxMilliC != 60000 || t0.CritMilliC != 70000 {
		t.Errorf("k10temp thresholds = %d/%d, want 60000/70000", t0.MaxMilliC, t0.CritMilliC)
	}
	if t1 := chips[1].Temps[0]; t1.MaxMilliC != -1 || t1.CritMilliC != -1 {
		t.Errorf("zero max must not validate: %+v", t1)
	}
	if t2 := chips[1].Temps[1]; t2.MaxMilliC != 50000 || t2.CritMilliC != -1 {
		t.Errorf("inverted crit must drop: %+v", t2)
	}
	if !IsCPUChip("coretemp") || IsCPUChip("nvme") || !IsDriveChip("drivetemp") {
		t.Error("chip classification broken")
	}
}

func TestParseZfsVersion(t *testing.T) {
	user, kmod := ParseZfsVersion(readFixture(t, filepath.Join(fixtureDirMH, "zfs-version.out")))
	if user != "2.2.2" || kmod != "2.4.1" {
		t.Errorf("zfs version = (%q, %q), want (2.2.2, 2.4.1)", user, kmod)
	}
	if u, k := ParseZfsVersion(""); u != "" || k != "" {
		t.Error("empty input should yield empty versions")
	}
}

func TestParseOsRelease(t *testing.T) {
	if got := ParseOsRelease(readFixture(t, filepath.Join(fixtureDirMH, "os-release.out"))); got != "ubuntu 24.04" {
		t.Errorf("os-release = %q, want ubuntu 24.04", got)
	}
	if got := ParseOsRelease("PRETTY_NAME=\"Debian GNU/Linux 12\"\n"); got != "Debian GNU/Linux 12" {
		t.Errorf("fallback = %q, want PRETTY_NAME", got)
	}
}

func TestNiceUptime(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{0, "-"}, {59, "0m"}, {3700, "1h 1m"}, {340116, "3d 22h"}, {7776000, "90d 0h"},
	}
	for _, c := range cases {
		if got := NiceUptime(c.sec); got != c.want {
			t.Errorf("NiceUptime(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}
