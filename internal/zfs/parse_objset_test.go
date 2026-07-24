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
}
