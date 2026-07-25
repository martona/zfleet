package zfs

import (
	"path/filepath"
	"testing"
)

func TestErrCount(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0}, {"-", 0}, {"", 0}, {"12", 12}, {"1.05K", 1075}, {"2M", 2097152},
	}
	for _, c := range cases {
		if got := ErrCount(c.in); got != c.want {
			t.Errorf("ErrCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPoolErrSums(t *testing.T) {
	status := readFixture(t, filepath.Join("../../testdata/fixtures/synthetic/sickhost", "zpool-status.out"))
	pools := ParseZpoolStatus(status)
	if len(pools) != 2 {
		t.Fatalf("pools = %d, want 2", len(pools))
	}
	var tank, cold *Pool
	for _, p := range pools {
		switch p.Name {
		case "tank":
			tank = p
		case "cold":
			cold = p
		}
	}
	if tank == nil || cold == nil {
		t.Fatal("tank/cold not parsed")
	}
	// cold is ONLINE with a single cksum error — the WARN-bubble case
	if r, w, c := cold.ErrSums(); r != 0 || w != 0 || c != 1 {
		t.Errorf("cold sums = %d %d %d, want 0 0 1", r, w, c)
	}
	// tank: R 12+3, W 98, C 2(raidz row)+1.05K(sda)+1
	r, w, c := tank.ErrSums()
	if r != 15 || w != 98 || c != 1075+3 {
		t.Errorf("tank sums = %d %d %d, want 15 98 %d", r, w, c, 1075+3)
	}
}
