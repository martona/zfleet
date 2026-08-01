package zfs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseObjsets(t *testing.T) {
	text := readFixture(t, filepath.Join(fixtureDir25, "objset-all.out"))
	m := ParseObjsets(text)
	if len(m) < 20 {
		t.Fatalf("objsets = %d, want ~25 (loaded datasets only)", len(m))
	}
	// note: rust root has NO objset — canmount=off, never mounted. Only
	// loaded datasets appear; the running OS trees must.
	var haveRpool bool
	for name, o := range m {
		if strings.HasPrefix(name, "rpool/") {
			haveRpool = true
		}
		if o.Reads < 0 || o.NWritten < 0 {
			t.Errorf("%s negative counters: %+v", name, o)
		}
		if strings.ContainsRune(name, '\t') {
			t.Errorf("unparsed name %q", name)
		}
	}
	if !haveRpool {
		t.Error("no rpool/* objsets — the running system's datasets should be loaded")
	}
	// the fixture's first objset, hand-checked values
	bb := m["bpool/BOOT/ubuntu_vethlw"]
	if bb.Writes != 1 || bb.NWritten != 1024 || bb.Reads != 2 || bb.NRead != 2048 {
		t.Errorf("bpool/BOOT/ubuntu_vethlw = %+v", bb)
	}
	// 2.2+ kmods carry per-dataset zil stats — the pool zil line's substrate
	var zilSeen bool
	for _, o := range m {
		if o.ZilSeen {
			zilSeen = true
			break
		}
	}
	if !zilSeen {
		t.Error("no objset carried zil stats — fixture kmod is 2.2+, ZilSeen should be set")
	}
}

func TestParseObjsetZilFields(t *testing.T) {
	// modeled on zeus rpool/objset-0x203 (the rasdaemon fsync storm)
	text := `29 1 0x01 21 5712 36063817597 312994588276363
name                            type data
dataset_name                    7    rpool/root
zil_commit_count                4    14721339
zil_itx_metaslab_normal_count   4    11158442
zil_itx_metaslab_normal_bytes   4    73221312528
zil_itx_metaslab_slog_count     4    0
zil_itx_metaslab_slog_bytes     4    0
writes                          4    5
nwritten                        4    512
`
	o := ParseObjsets(text)["rpool/root"]
	if !o.ZilSeen || o.ZilCommits != 14721339 || o.ZilNormalC != 11158442 ||
		o.ZilNormalB != 73221312528 || o.ZilSlogC != 0 || o.ZilSlogB != 0 {
		t.Fatalf("zil fields = %+v", o)
	}
	if o.Writes != 5 || o.NWritten != 512 {
		t.Fatalf("io fields disturbed by zil parse: %+v", o)
	}
}
