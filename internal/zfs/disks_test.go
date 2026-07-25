package zfs

import (
	"path/filepath"
	"strings"
	"testing"
)

const fixtureDirDisk = "../../testdata/fixtures/commodoreplus4/2026-07-25-disk"

func loadDiskSurfaces(t *testing.T) (map[string]string, map[string]BlockNode, []Disk) {
	t.Helper()
	aliases := ParseDiskAliases(readFixture(t, filepath.Join(fixtureDirDisk, "disk-aliases.out")))
	blocks := ParseSysBlock(readFixture(t, filepath.Join(fixtureDirDisk, "sys-block.out")))
	disks := ParseLsblkDisks(readFixture(t, filepath.Join(fixtureDirDisk, "lsblk-disks.out")))
	return aliases, blocks, disks
}

func TestDiskSurfaces(t *testing.T) {
	aliases, blocks, disks := loadDiskSurfaces(t)
	if len(aliases) < 100 {
		t.Errorf("aliases = %d, want lots", len(aliases))
	}
	for base := range aliases {
		if strings.HasPrefix(base, "loop") {
			t.Errorf("loop alias leaked: %s", base)
		}
	}
	// zfs whole-disk members carry a hidden 8M partition 9
	if b, ok := blocks["sdk9"]; !ok || b.Parent != "sdk" {
		t.Errorf("sdk9 parent = %+v, want sdk", b)
	}
	if b := blocks["sdk"]; b.Parent != "" || !strings.HasPrefix(b.DevPath, "devices/") {
		t.Errorf("sdk = %+v, want whole-disk with devices/ path", b)
	}
	for _, d := range disks {
		if strings.HasPrefix(d.Node, "loop") || strings.HasPrefix(d.Node, "zd") {
			t.Errorf("non-disk leaked into inventory: %s", d.Node)
		}
	}
	// cp4: 2×8 rust spinners + specials + boot devices — well over a dozen
	if len(disks) < 16 {
		t.Errorf("disks = %d, want >= 16", len(disks))
	}
}

// Every leaf of every real pool must resolve to a whole-disk node that the
// inventory knows — the mapper's entire job, proven on live topology.
func TestResolveVdevRealPools(t *testing.T) {
	aliases, blocks, disks := loadDiskSurfaces(t)
	pools := ParseZpoolStatus(readFixture(t, filepath.Join(fixtureDirDisk, "zpool-status.out")))
	known := map[string]bool{}
	for _, d := range disks {
		known[d.Node] = true
	}
	leaves := 0
	for _, p := range pools {
		for _, c := range p.Classes {
			for _, v := range c.Vdevs {
				for _, leaf := range v.Leaves() {
					if len(leaf.Children) > 0 {
						continue
					}
					leaves++
					node := ResolveVdev(leaf.Name, aliases, blocks)
					if node == "" {
						t.Errorf("unresolved leaf %s", leaf.Name)
						continue
					}
					if !known[node] {
						t.Errorf("leaf %s resolved to %s, not in inventory", leaf.Name, node)
					}
				}
			}
		}
	}
	if leaves < 20 {
		t.Errorf("leaves = %d, want >= 20 (rust alone has 16)", leaves)
	}
}

// The lemur shapes: partlabel members, EUI by-id with -part suffixes, and
// virtualboy's bare whole-disk by-id — three schemes, one resolver.
func TestResolveVdevSynthetic(t *testing.T) {
	aliases := ParseDiskAliases(
		"/dev/disk/by-partlabel/PDL-BPOOL-B ../../nvme0n1p5\n" +
			"/dev/disk/by-id/nvme-eui.0025384c41a0210b-part3 ../../nvme1n1p3\n" +
			"/dev/disk/by-id/nvme-Sabrent_SB-RKT4P-8TB_48821072200112 ../../nvme0n1\n")
	blocks := ParseSysBlock(
		"nvme0n1 ../../devices/pci0000:00/0000:00:02.0/nvme/nvme0/nvme0n1\n" +
			"nvme0n1p5 ../../devices/pci0000:00/0000:00:02.0/nvme/nvme0/nvme0n1/nvme0n1p5\n" +
			"nvme1n1 ../../devices/pci0000:00/0000:00:03.0/nvme/nvme1/nvme1n1\n" +
			"nvme1n1p3 ../../devices/pci0000:00/0000:00:03.0/nvme/nvme1/nvme1n1/nvme1n1p3\n" +
			"sdc ../../devices/pci0000:00/ata9/host9/target9:0:0/9:0:0:0/block/sdc\n")
	cases := []struct{ leaf, want string }{
		{"PDL-BPOOL-B", "nvme0n1"},                              // partlabel → partition → disk
		{"nvme-eui.0025384c41a0210b-part3", "nvme1n1"},          // EUI by-id partition
		{"nvme-Sabrent_SB-RKT4P-8TB_48821072200112", "nvme0n1"}, // whole-disk by-id
		{"sdc", "sdc"},                // bare kernel name
		{"/tmp/file-vdev.img", ""},    // file vdev: blank, not wrong
		{"mystery-alias-nowhere", ""}, // unknown alias
	}
	for _, c := range cases {
		if got := ResolveVdev(c.leaf, aliases, blocks); got != c.want {
			t.Errorf("ResolveVdev(%s) = %q, want %q", c.leaf, got, c.want)
		}
	}
}

// The real lemur: PDL partlabels and EUI by-id names on a two-NVMe laptop
// mirror — the mixed-scheme specimen, resolved against its own captured
// alias universe.
func TestResolveVdevLemur(t *testing.T) {
	dir := "../../testdata/fixtures/lemur/2026-07-25-disk"
	aliases := ParseDiskAliases(readFixture(t, filepath.Join(dir, "disk-aliases.out")))
	blocks := ParseSysBlock(readFixture(t, filepath.Join(dir, "sys-block.out")))
	pools := ParseZpoolStatus(readFixture(t, filepath.Join(dir, "zpool-status.out")))
	resolved := map[string]string{}
	for _, p := range pools {
		for _, c := range p.Classes {
			for _, v := range c.Vdevs {
				for _, leaf := range v.Leaves() {
					if len(leaf.Children) > 0 {
						continue
					}
					resolved[leaf.Name] = ResolveVdev(leaf.Name, aliases, blocks)
				}
			}
		}
	}
	if len(resolved) != 4 {
		t.Fatalf("leaves = %d, want 4 (two mirrored pools)", len(resolved))
	}
	for leaf, node := range resolved {
		if node != "nvme0n1" && node != "nvme1n1" {
			t.Errorf("leaf %s resolved to %q, want a lemur nvme", leaf, node)
		}
	}
	if resolved["PDL-BPOOL-B"] != "nvme1n1" {
		t.Errorf("PDL-BPOOL-B = %q, want nvme1n1", resolved["PDL-BPOOL-B"])
	}
}

func TestParseHwmonDevs(t *testing.T) {
	devs := ParseHwmonDevs(readFixture(t, filepath.Join(fixtureDirDisk, "hwmon-dev.out")))
	if len(devs) != 3 {
		t.Fatalf("devs = %d, want 3", len(devs))
	}
	for dir, path := range devs {
		if !strings.HasPrefix(dir, "/sys/class/hwmon/hwmon") || !strings.HasPrefix(path, "devices/") {
			t.Errorf("odd entry: %s → %s", dir, path)
		}
	}
}
