package ui

import (
	"testing"
	"time"
)

// The tier state machine: dwell is the debounce (passing through promotes
// nothing), the grace is the hysteresis (excursions don't thrash), and
// promotion zeroes the due gates so collectors fire at their next tick.
func TestTickTier(t *testing.T) {
	tun := DefaultTuning()
	h := newHostState("x", "", nil)
	now := time.Unix(1000, 0)

	// cursor passing through (no dwell) never promotes
	h.tickTier(true, false, now, tun)
	if h.fg {
		t.Fatal("promoted without dwell — debounce dead")
	}

	// parked cursor promotes and zeroes the dues
	h.statsDue = now.Add(time.Hour)
	h.tickTier(true, true, now, tun)
	if !h.fg || !h.statsDue.IsZero() {
		t.Fatalf("dwelled selection: fg=%v statsDue=%v, want promoted with zero dues", h.fg, h.statsDue)
	}

	// cursor leaves: fg survives the grace, then drops
	h.tickTier(false, true, now.Add(tun.Demote-time.Second), tun)
	if !h.fg {
		t.Fatal("demoted inside the grace window")
	}
	h.tickTier(false, true, now.Add(tun.Demote+time.Second), tun)
	if h.fg {
		t.Fatal("still fg after the grace expired")
	}

	// never-selected host is bg from birth
	v := newHostState("y", "", nil)
	v.tickTier(false, true, now, tun)
	if v.fg {
		t.Fatal("unselected host promoted")
	}

	// cadence picks by tier, flooring bg at the fg base
	if iv := h.cadence(2*time.Second, tun.BgStats); iv != tun.BgStats {
		t.Fatalf("bg cadence = %v, want %v", iv, tun.BgStats)
	}
	h.fg = true
	if iv := h.cadence(2*time.Second, tun.BgStats); iv != 2*time.Second {
		t.Fatalf("fg cadence = %v, want 2s", iv)
	}
	h.fg = false
	if iv := h.cadence(10*time.Second, 5*time.Second); iv != 10*time.Second {
		t.Fatalf("bg faster than fg not floored: %v", iv)
	}
}
