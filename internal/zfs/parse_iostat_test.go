package zfs

import "testing"

func TestIostatTimestamp(t *testing.T) {
	cases := map[string]bool{
		"1785431442":           true,
		"0":                    true,
		"":                     false,
		"rpool\t100\t200":      false,
		"1785431442\textra":    false,
		"mirror-0":             false,
		"12345678901234567890": true, // any bare digit run frames
	}
	for line, want := range cases {
		if got := IostatTimestamp(line); got != want {
			t.Errorf("IostatTimestamp(%q) = %v, want %v", line, got, want)
		}
	}
}

// Pool rows must parse from BOTH forms: the 7-column -Hpy one-shot of old
// fixtures and the 18-column -Hpvly streaming rows (first 7 fields agree).
func TestParseIostatPoolsWideRows(t *testing.T) {
	names := map[string]bool{"rpool": true}
	wide := "rpool\t19226529792\t94590103552\t0\t123\t0\t1591214\t-\t497489\t-\t117208\t-\t720\t-\t420476\t-\t-\t-\t-\n" +
		"mirror-0\t19226529792\t94590103552\t0\t123\t0\t1591219\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\n"
	io := ParseIostatPools(wide, names)
	r, ok := io["rpool"]
	if !ok || r.WOps != 123 || r.WBw != 1591214 {
		t.Fatalf("wide rpool row = %+v ok=%v, want wops 123 wbw 1591214", r, ok)
	}
	if len(io) != 1 {
		t.Fatalf("vdev rows leaked into pool io: %v", io)
	}
}
