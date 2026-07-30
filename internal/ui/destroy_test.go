package ui

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The F8 state machine without a terminal: rows built from mark groups,
// the guards (root-fs, sudo, unresolved), and the completion bookkeeping —
// successes drop marks and caches and zero the cadence dues, failures keep
// their marks and carry their reason.

func destroyFixture() (*Model, *hostState) {
	m, h := marksFixture()
	m.snapsShown = map[string]bool{}
	h.sudoOK, h.sudoProbed = true, true
	return m, h
}

func TestOpenDestroyPopup(t *testing.T) {
	m, h := destroyFixture()
	m.marks[treeDsID(h, "p/a/b")] = true
	m.marks[treeDsID(h, "p/a@s1")] = true
	m.marks[treeDsID(h, "p/a@s2")] = true
	m.markGen++

	m.OpenDestroyPopup()
	if !m.destroyPop || len(m.destroyRows) != 2 {
		t.Fatalf("popup=%v rows=%d, want open with 2 rows", m.destroyPop, len(m.destroyRows))
	}
	snap, ds := m.destroyRows[0], m.destroyRows[1]
	if snap.dsMark || snap.target != "p/a@s1,s2" || snap.nsnaps != 2 || snap.blocked != "" {
		t.Fatalf("snap row = %+v, want unblocked p/a@s1,s2 ×2", snap)
	}
	if got := snap.cmdString(); got != "sudo -n zfs destroy p/a@s1,s2" {
		t.Fatalf("snap cmd = %q", got)
	}
	if !ds.dsMark || ds.target != "p/a/b" || ds.blocked != "" {
		t.Fatalf("ds row = %+v, want unblocked -r p/a/b", ds)
	}
	if got := ds.cmdString(); got != "sudo -n zfs destroy -r p/a/b" {
		t.Fatalf("ds cmd = %q", got)
	}
}

func TestDestroyGuards(t *testing.T) {
	m, h := destroyFixture()
	tree := h.dsTrees["p"]
	// p/a/c is the running root: mounted at /
	tree.ByName["p/a/c"].Mounted, tree.ByName["p/a/c"].Mountpoint = "yes", "/"
	// p/a/d is a stale boot environment: mountpoint=/ but NOT mounted
	tree.ByName["p/a/d"].Mounted, tree.ByName["p/a/d"].Mountpoint = "no", "/"

	m.marks[treeDsID(h, "p/a")] = true // subtree contains the live root
	m.markGen++
	m.OpenDestroyPopup()
	if len(m.destroyRows) != 1 || !strings.Contains(m.destroyRows[0].blocked, "running system") {
		t.Fatalf("marked ancestor of live root: %+v, want blocked", m.destroyRows)
	}

	// the stale BE alone is destroyable — mountpoint=/ must not block it
	m.clearMarks()
	m.marks[treeDsID(h, "p/a/d")] = true
	m.markGen++
	m.OpenDestroyPopup()
	if len(m.destroyRows) != 1 || m.destroyRows[0].blocked != "" {
		t.Fatalf("stale boot environment blocked: %+v, want runnable", m.destroyRows)
	}

	// no sudo: shown but unexecutable, and enter must refuse it
	h.sudoOK = false
	m.OpenDestroyPopup()
	if m.destroyRows[0].blocked != "no sudo" {
		t.Fatalf("blocked = %q, want no sudo", m.destroyRows[0].blocked)
	}
	if cmd := m.destroyKeys("enter"); cmd != nil || m.destroyRows[0].status != destroyIdle {
		t.Fatal("enter executed a blocked row")
	}
	if got := m.destroyRows[0].cmdString(); got != "zfs destroy -r p/a/d" {
		t.Fatalf("no-sudo cmd = %q, want bare zfs", got)
	}
}

func TestDestroyDoneSuccess(t *testing.T) {
	m, h := destroyFixture()
	m.marks[treeDsID(h, "p/a@s1")] = true
	m.marks[treeDsID(h, "p/a@s2")] = true
	m.marks[treeDsID(h, "p/a/b")] = true
	m.markGen++
	m.OpenDestroyPopup()

	h.statsDue = time.Now().Add(time.Hour)
	h.dryCache["p/a@s1,s2"] = &dryResult{text: "reclaim 42"}
	m.destroyRows[0].status = destroyRunning
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: "p/a@s1,s2"})

	wantMarks(t, m, h, "p/a/b") // the snaps left, the dataset mark stays
	if m.destroyRows[0].status != destroyDone {
		t.Fatalf("row status = %d, want done", m.destroyRows[0].status)
	}
	if !h.statsDue.IsZero() || !h.poolsDue.IsZero() || !h.dsDue.IsZero() {
		t.Fatal("cadence dues not zeroed — refresh would wait out the background tier")
	}
	if len(h.dryCache) != 0 {
		t.Fatal("dry cache survived — neighbors' reclaim math is stale now")
	}

	// dataset destroy sweeps the subtree's snapshot caches and t-toggles
	m.snapsShown[treeDsID(h, "p/a/b")] = true
	m.destroyRows[1].status = destroyRunning
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: "p/a/b"})
	wantMarks(t, m, h)
	if _, ok := h.dsSnaps["p/a/b"]; ok {
		t.Fatal("destroyed dataset's snapshot cache survived")
	}
	if m.snapsShown[treeDsID(h, "p/a/b")] {
		t.Fatal("destroyed dataset's t-toggle survived")
	}
}

func TestDestroyDoneFailure(t *testing.T) {
	m, h := destroyFixture()
	m.marks[treeDsID(h, "p/a@s1")] = true
	m.markGen++
	m.OpenDestroyPopup()
	m.destroyRows[0].status = destroyRunning

	m.applyDestroyDone(destroyDoneMsg{host: "h", target: "p/a@s1",
		text: "cannot destroy snapshot p/a@s1: snapshot has dependent clones\nuse '-R'",
		err:  errors.New("exit status 1")})

	wantMarks(t, m, h, "p/a@s1") // failures keep their marks
	r := m.destroyRows[0]
	if r.status != destroyFailed || !strings.Contains(r.errText, "dependent clones") {
		t.Fatalf("row = %+v, want failed with the zfs reason", r)
	}
	if e := m.destroyErrs[destroyKey(h, "p/a")]; !strings.Contains(e, "dependent clones") {
		t.Fatalf("destroyErrs = %q, want first line of the failure", e)
	}
	m.clearMarks()
	if len(m.destroyErrs) != 0 {
		t.Fatal("failure notes outlived the selection")
	}
}

func TestDestroyCmdDisplay(t *testing.T) {
	r := destroyRow{ds: "p/a", target: "p/a@snap-001,snap-002,snap-003,snap-004", nsnaps: 4}
	full := "zfs destroy p/a@snap-001,snap-002,snap-003,snap-004"
	if got := destroyCmdDisplay(r, 80); got != full {
		t.Fatalf("unelided = %q", got)
	}
	got := destroyCmdDisplay(r, 40)
	if utf8.RuneCountInString(got) > 40 || !strings.Contains(got, ",…(+") ||
		!strings.HasPrefix(got, "zfs destroy p/a@snap-001") {
		t.Fatalf("elided = %q (width %d), want head intact + count", got, utf8.RuneCountInString(got))
	}
	// zvol-style dataset mark: no @, plain truncate is the only option
	d := destroyRow{ds: "p/very/deep/dataset/path/that/keeps/going", dsMark: true,
		target: "p/very/deep/dataset/path/that/keeps/going"}
	if got := destroyCmdDisplay(d, 30); utf8.RuneCountInString(got) > 30 {
		t.Fatalf("ds elision overflowed: %q", got)
	}
}

func TestDestroySequentialPerHost(t *testing.T) {
	m, h := destroyFixture()
	m.marks[treeDsID(h, "p/a@s1")] = true
	m.marks[treeDsID(h, "p/a/b")] = true
	m.markGen++
	m.OpenDestroyPopup()

	// shift+F8 starts exactly one command on the host, chains the next on
	// completion, and the chain dies with the popup
	m.destroyKeys("f20")
	running := 0
	for _, r := range m.destroyRows {
		if r.status == destroyRunning {
			running++
		}
	}
	if running != 1 || !m.destroyAll {
		t.Fatalf("running = %d destroyAll = %v, want 1 in flight", running, m.destroyAll)
	}
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: m.destroyRows[0].target})
	if m.destroyRows[1].status != destroyRunning {
		t.Fatal("chain did not continue to the host's next row")
	}

	// closing the popup kills the chain (in-flight results still land)
	m.destroyKeys("esc")
	if m.destroyPop || m.destroyAll {
		t.Fatal("esc left the popup or the chain armed")
	}
}
