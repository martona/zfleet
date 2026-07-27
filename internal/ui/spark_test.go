package ui

import (
	"strings"
	"testing"
)

// sparklineTall carries the perf screen's io charts: two braille rows scaled
// to an explicit high-water ceiling instead of the window max, so history
// rotating out of the ring can't reinflate the recent past.
func TestSparklineTall(t *testing.T) {
	rows := sparklineTall(sparkSteel, []int64{0, 0, 500, 1000}, 2, 2, 1000)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	top, bot := []rune(stripSGR(rows[0])), []rune(stripSGR(rows[1]))
	if len(top) != 2 || len(bot) != 2 {
		t.Fatalf("cell widths = %d/%d, want 2/2", len(top), len(bot))
	}
	// zeros: baseline dot on the bottom row only, blank air above
	if top[0] != 0x2800 {
		t.Errorf("top-left = %U, want blank braille", top[0])
	}
	if bot[0] == 0x2800 || bot[0] == ' ' {
		t.Errorf("bottom-left = %U, want a baseline dot", bot[0])
	}
	// a full-ceiling sample must ink the top row
	if top[1] == 0x2800 {
		t.Errorf("top-right = %U, want ink for a ceiling-high sample", top[1])
	}

	// the ceiling holds the scale: the same values against a 10× ceiling
	// must not reach the top row (deflation protection is the whole point)
	rows = sparklineTall(sparkSteel, []int64{0, 0, 500, 1000}, 2, 2, 10000)
	top = []rune(stripSGR(rows[0]))
	if top[1] != 0x2800 {
		t.Errorf("top-right = %U, want blank under a 10× ceiling", top[1])
	}

	// short history right-aligns: one sample in a width-4 chart pads left
	rows = sparklineTall(sparkGold, []int64{700}, 4, 2, 1000)
	bot = []rune(stripSGR(rows[1]))
	if len(bot) != 4 {
		t.Fatalf("width = %d, want 4", len(bot))
	}
	if !strings.HasPrefix(string(bot), "   ") {
		t.Errorf("short history should left-pad: %q", string(bot))
	}
}
