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

func TestParseHwmonTemp(t *testing.T) {
	// the fixture's hottest sensor is the 10GbE NIC at 64°C; the hottest
	// preferred one is Xeon package 1 at 45°C — the NIC must lose
	c, src, ok := ParseHwmonTemp(readFixture(t, filepath.Join(fixtureDirMH, "hwmon.out")))
	if !ok || c != 45 || src != "cpu" {
		t.Errorf("hwmon = (%d, %q, %v), want (45, cpu, true)", c, src, ok)
	}
}

func TestParseHwmonTempSynthetic(t *testing.T) {
	nvme := "/sys/class/hwmon/hwmon3/name:nvme\n" +
		"/sys/class/hwmon/hwmon3/temp1_input:53850\n" +
		"/sys/class/hwmon/hwmon4/name:k10temp\n" +
		"/sys/class/hwmon/hwmon4/temp1_input:51000\n"
	if c, src, ok := ParseHwmonTemp(nvme); !ok || c != 54 || src != "nvme" {
		t.Errorf("nvme case = (%d, %q, %v), want (54, nvme, true)", c, src, ok)
	}
	nicOnly := "/sys/class/hwmon/hwmon0/name:enp9s0\n" +
		"/sys/class/hwmon/hwmon0/temp1_input:64421\n"
	if _, _, ok := ParseHwmonTemp(nicOnly); ok {
		t.Error("a NIC alone must not become the host temperature")
	}
	if _, _, ok := ParseHwmonTemp(""); ok {
		t.Error("empty input must report no temperature")
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
