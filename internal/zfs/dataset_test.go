package zfs

import (
	"path/filepath"
	"strings"
	"testing"
)

// fixtureDir25 has the v2 captures: wide list with logicalused/
// logicalreferenced, plus zpool-ashift.out.
const fixtureDir25 = "../../testdata/fixtures/commodoreplus4/2026-07-25"

func loadTree(t *testing.T) *DatasetTree {
	t.Helper()
	return ParseDatasets(readFixture(t, filepath.Join(fixtureDir25, "zfs-list-wide.out")))
}

func TestParseDatasets(t *testing.T) {
	tree := loadTree(t)
	if len(tree.Roots) != 3 {
		t.Fatalf("roots = %d, want 3 (bpool, rpool, rust)", len(tree.Roots))
	}

	rust := tree.ByName["rust"]
	if rust == nil || rust.UsedChild < rust.Used*9/10 {
		t.Fatalf("rust root: %+v", rust)
	}
	if rust.Mounted != "no" || rust.Canmount != "off" {
		t.Errorf("rust mount state = %s/%s, want no/off", rust.Mounted, rust.Canmount)
	}

	recv := tree.ByName["rust/recv"]
	if recv == nil || len(recv.Children) != 11 {
		t.Fatalf("rust/recv children = %d, want 11", len(recv.Children))
	}
	if recv.Encryption != "aes-256-gcm" || recv.Keystatus != "available" || recv.Locked() {
		t.Errorf("recv encryption = %s/%s", recv.Encryption, recv.Keystatus)
	}

	bb := tree.ByName["rust/recv/bergamo-tiny-borgbackup"]
	if bb == nil || bb.UsedSnap != 774963712 {
		t.Fatalf("borgbackup usedbysnapshots: %+v", bb)
	}
	if !bb.Locked() {
		t.Errorf("borgbackup should be locked, keystatus = %s", bb.Keystatus)
	}

	nuggy := tree.ByName["rust/recv/bergamo-tiny-zvols/iscsi-nuggy"]
	if nuggy == nil || !nuggy.IsVolume() {
		t.Fatalf("nuggy: %+v", nuggy)
	}
	if nuggy.Origin != "rust/recv/bergamo-tiny-zvols/iscsi-nugget@tiptop" {
		t.Errorf("nuggy origin = %q", nuggy.Origin)
	}
	if nuggy.Volsize != 1099511627776 || nuggy.RefReserv != 0 {
		t.Errorf("nuggy volsize/refreserv = %d/%d (want 1T sparse)", nuggy.Volsize, nuggy.RefReserv)
	}

	mars := tree.ByName["rust/recv/bergamo/tiny/zvols/vm-mars"]
	if mars == nil || mars.LogicalUsed != 386703004160 {
		t.Fatalf("vm-mars logicalused: %+v", mars)
	}
	// the measured 16K-on-raidz3-8w inflation this fixture demonstrates
	if ratio := float64(mars.Used) / float64(mars.LogicalUsed); ratio < 1.13 || ratio > 1.15 {
		t.Errorf("vm-mars charged/logical = %.3f, want ~1.14", ratio)
	}
}

func TestParseSnapshots(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir25, "zfs-list-snapshots.out"))

	bb := ParseSnapshots(text, "rust/recv/bergamo-tiny-borgbackup")
	if len(bb) != 9 {
		t.Fatalf("borgbackup snaps = %d, want 9", len(bb))
	}
	for i := 1; i < len(bb); i++ {
		if bb[i].Creation < bb[i-1].Creation {
			t.Fatalf("snaps not chronological at %d", i)
		}
	}
	// child dataset snapshots must not leak into the parent's list
	root := ParseSnapshots(text, "rust")
	if len(root) != 0 {
		t.Errorf("rust root snaps = %d, want 0", len(root))
	}
}

func TestParseAllSnapshots(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir25, "zfs-list-snapshots.out"))
	all := ParseAllSnapshots(text)
	bb := all["rust/recv/bergamo-tiny-borgbackup"]
	per := ParseSnapshots(text, "rust/recv/bergamo-tiny-borgbackup")
	if len(bb) != len(per) || len(bb) == 0 {
		t.Fatalf("grouped %d vs per-ds %d", len(bb), len(per))
	}
	for i := range per {
		if per[i].Name != bb[i].Name {
			t.Fatalf("order diverges from ParseSnapshots at %d", i)
		}
	}
	if _, ok := all["rust"]; ok {
		t.Error("pool root gained snapshots it does not have")
	}
	// synthetic 5-col rows: written survives grouping, order is chronological
	syn := ParseAllSnapshots("a/b@s1\t1\t2\t100\t7\na/b@s0\t1\t2\t50\t9\n")
	if s := syn["a/b"]; len(s) != 2 || s[0].Snap != "s0" || s[0].Written != 9 || s[1].Written != 7 {
		t.Fatalf("synthetic parse = %+v", syn["a/b"])
	}
}

func TestGroupSnapshots(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir25, "zfs-list-snapshots.out"))

	// vm-mars: 2 named milestones + 6 zfsrecvd-* -> 3 rows
	mars := ParseSnapshots(text, "rust/recv/bergamo/tiny/zvols/vm-mars")
	entries := GroupSnapshots(mars, 3)
	var fams, singles int
	for _, e := range entries {
		if e.Fam != nil {
			fams++
			if e.Fam.Label() != "zfsrecvd-*" {
				t.Errorf("family label = %q", e.Fam.Label())
			}
			if len(e.Fam.Snaps) != 6 {
				t.Errorf("family size = %d, want 6", len(e.Fam.Snaps))
			}
		} else {
			singles++
		}
	}
	if fams != 1 || singles != 2 {
		t.Errorf("vm-mars rows = %d families + %d singles, want 1+2", fams, singles)
	}

	// pure-timestamp names group under the "*" label
	optane := ParseSnapshots(text, "rust/recv/virtualboy-optanepool-optane")
	oe := GroupSnapshots(optane, 3)
	if len(oe) != 1 || oe[0].Fam == nil || oe[0].Fam.Label() != "*" {
		t.Fatalf("optane grouping = %+v", oe)
	}
	if got := len(oe[0].Fam.Snaps); got != 4 {
		t.Errorf("optane family size = %d, want 4", got)
	}
}

func TestParseProps(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir25, "zfs-get-all-fs.out"))
	props := ParseProps(text, "rust/recv/bergamo/tiny/timemachine")
	if len(props) < 40 {
		t.Fatalf("props = %d, want plenty", len(props))
	}
	if p := props["compression"]; p.Value != "lz4" || !strings.Contains(p.Source, "inherited") {
		t.Errorf("compression = %+v", p)
	}
}
