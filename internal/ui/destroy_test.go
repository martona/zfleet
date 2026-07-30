package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The F8 state machine without a terminal: rows built from mark groups,
// the guards (root-fs, sudo, unresolved), and the completion bookkeeping —
// successes drop marks and caches and zero the cadence dues, failures keep
// their marks and carry their reason.

func destroyFixture() (*Model, *hostState) {
	m, h := marksFixture()
	m.snapsShown = map[string]bool{}
	m.expanded = map[string]bool{}
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

func TestDestroySigma(t *testing.T) {
	m, h := destroyFixture()
	h.dsTrees["p"].ByName["p/a/b"].Used = 2 << 20
	m.destroyRows = []destroyRow{
		{h: h, ds: "p/a", target: "p/a@s1,s2,s3", nsnaps: 3, reclaim: 3 << 20, haveReclaim: true},
		{h: h, ds: "p/a/b", dsMark: true, target: "p/a/b"},
		{h: h, ds: "p/a/c", dsMark: true, target: "p/a/c", blocked: "no sudo"},
	}
	if got, want := m.destroySigma(), "reclaims "+zfs.NiceBytes(5<<20); got != want {
		t.Fatalf("untouched = %q, want %q", got, want)
	}
	// object counts expose partial completion even when bytes agree
	m.destroyRows[0].status = destroyDone
	want := fmt.Sprintf("reclaimed 3/4 (%s/%s)", zfs.NiceBytes(3<<20), zfs.NiceBytes(5<<20))
	if got := m.destroySigma(); got != want {
		t.Fatalf("in progress = %q, want %q", got, want)
	}
	// a failure pins the ledger in progress form forever
	m.destroyRows[1].status = destroyFailed
	if got := m.destroySigma(); got != fmt.Sprintf("reclaimed 3/4 (%s/%s)",
		zfs.NiceBytes(3<<20), zfs.NiceBytes(5<<20)) {
		t.Fatalf("with failure = %q, want the in-progress form", got)
	}
	// 100% success: the plain figure
	m.destroyRows[1].status = destroyDone
	if got, want := m.destroySigma(), "reclaimed "+zfs.NiceBytes(5<<20); got != want {
		t.Fatalf("complete = %q, want %q", got, want)
	}
}

func TestTruncateNegative(t *testing.T) {
	// deep tree indents in a narrow pane hand renderers negative budgets;
	// truncate must be total (the live crash: slice bounds [:-1])
	for _, w := range []int{-5, -1, 0, 1, 2} {
		got := truncate("@autosnap-2026", w)
		if utf8.RuneCountInString(got) > max(w, 0) {
			t.Fatalf("truncate(w=%d) = %q, wider than budget", w, got)
		}
	}
}

func TestWrapErr(t *testing.T) {
	// grouped destroys error once per snapshot — the unfold must split on
	// the real newlines before hard-wrapping, or raw \n tears the frame
	in := "cannot destroy snapshot a@s1: dataset is busy\ncannot destroy snapshot a@s2: dataset is busy\r\n\n"
	out := wrapErr(in, 30)
	if len(out) < 2 {
		t.Fatalf("wrapErr = %d lines, want the two errors split", len(out))
	}
	for _, l := range out {
		if strings.ContainsAny(l, "\n\r") {
			t.Fatalf("line %q carries a raw newline into the overlay", l)
		}
		if utf8.RuneCountInString(l) > 30 {
			t.Fatalf("line %q exceeds the wrap width", l)
		}
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

	// shift+F8 starts one command on the host, queues the rest, and the
	// queue advances on completion
	m.destroyKeys("f20")
	if m.destroyRows[0].status != destroyRunning || m.destroyRows[1].status != destroyQueued {
		t.Fatalf("after f20: %d/%d, want running/queued",
			m.destroyRows[0].status, m.destroyRows[1].status)
	}
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: m.destroyRows[0].target})
	if m.destroyRows[1].status != destroyRunning {
		t.Fatal("queue did not advance to the host's next row")
	}
	m.destroyKeys("esc")
	if m.destroyPop {
		t.Fatal("esc left the popup open")
	}
}

func TestDestroyEnterQueues(t *testing.T) {
	m, h := destroyFixture()
	m.marks[treeDsID(h, "p/a@s1")] = true
	m.marks[treeDsID(h, "p/a/b")] = true
	m.markGen++
	m.OpenDestroyPopup()

	// enter on a busy host queues instead of doing nothing
	m.destroyKeys("enter") // row 0 runs
	m.destroyKeys("down")
	m.destroyKeys("enter") // host busy: row 1 queues
	if m.destroyRows[1].status != destroyQueued {
		t.Fatalf("enter on busy host: status %d, want queued", m.destroyRows[1].status)
	}
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: m.destroyRows[0].target})
	if m.destroyRows[1].status != destroyRunning {
		t.Fatal("queued row did not start after the host freed up")
	}

	// esc empties the queue so a reopened popup starts clean
	m.destroyRows[1].status = destroyQueued
	m.destroyKeys("esc")
	if m.destroyRows[1].status != destroyIdle {
		t.Fatal("esc did not demote the queued row")
	}
}

func TestDestroyCursorRest(t *testing.T) {
	m, h := destroyFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	m.expanded[treePoolID(h, "p")] = true
	m.expanded[treeDsID(h, "p")] = true
	m.expanded[treeDsID(h, "p/a")] = true
	m.snapsShown[treeDsID(h, "p/a")] = true
	m.marks[treeDsID(h, "p/a@s1")] = true
	m.markGen++
	m.OpenDestroyPopup()

	// cursor sits on the doomed snapshot; after the destroy it must rest
	// on the row immediately above, not jump to the overview
	m.rowsOK = false
	m.treeSel = treeDsID(h, "p/a@s1")
	rows := m.treeRows()
	want := ""
	for i, r := range rows {
		if r.id == m.treeSel && i > 0 {
			want = rows[i-1].id
		}
	}
	if want == "" {
		t.Fatalf("fixture broken: snap row not found in %d rows", len(rows))
	}
	m.destroyRows[0].status = destroyRunning
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: "p/a@s1"})
	if m.treeSel != want {
		t.Fatalf("cursor = %q, want the row above the casualty %q", m.treeSel, want)
	}

	// a cursor elsewhere is left alone
	m.rowsOK = false
	m.marks[treeDsID(h, "p/a@s2")] = true
	m.markGen++
	m.OpenDestroyPopup()
	m.treeSel = treeDsID(h, "p/a/b")
	m.destroyRows[0].status = destroyRunning
	m.applyDestroyDone(destroyDoneMsg{host: "h", target: "p/a@s2"})
	if m.treeSel != treeDsID(h, "p/a/b") {
		t.Fatalf("uninvolved cursor moved to %q", m.treeSel)
	}
}
