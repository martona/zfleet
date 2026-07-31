package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// zpool counters in the warnings inbox: one line per counter-bearing
// leaf (pool-wide for interior counters), the line is the verbatim clear
// command, enter runs it, success retires the line when the refetch
// confirms, failure wears its reason.

func clearFixture() (*Model, *hostState) {
	m, h := destroyFixture()
	h.pools = []*zfs.Pool{{Name: "p", Classes: []*zfs.VdevClass{{
		Name: "data",
		Vdevs: []*zfs.Vdev{{Name: "raidz1-0", Children: []*zfs.Vdev{
			{Name: "sda", CksumErr: "1"},
			{Name: "sdb"},
		}}},
	}}}}
	return m, h
}

func TestClearInbox(t *testing.T) {
	m, h := clearFixture()
	m.OpenAckPopup()
	if !m.ackPop || len(m.ackList) != 1 {
		t.Fatalf("popup=%v entries=%d, want 1 clear line", m.ackPop, len(m.ackList))
	}
	e := m.ackList[0]
	if e.pool != "p" || e.vdev != "sda" || e.clearResolve() != "C 1" {
		t.Fatalf("entry = %+v (badge %q), want p/sda C 1", e, e.clearResolve())
	}
	if got := clearCmd(e); got != "sudo -n zpool clear p sda" {
		t.Fatalf("cmd = %q", got)
	}

	// enter runs the shown command
	cmd := m.ackKeys("enter")
	if cmd == nil || m.ackList[0].clearSt != clearRunning {
		t.Fatal("enter did not launch the clear")
	}

	// success: pool refetch armed NOW, line rides until the data confirms
	h.poolsDue = h.poolsDue.Add(1) // any nonzero
	m.applyPoolClear(poolClearMsg{host: "h", pool: "p", vdev: "sda"})
	if m.ackList[0].clearSt != clearDone || !h.poolsDue.IsZero() || !h.poolsPend {
		t.Fatalf("after success: st=%d due=%v pend=%v", m.ackList[0].clearSt, h.poolsDue, h.poolsPend)
	}
	// the refetch lands with clean counters: the line retires, popup closes
	h.pools[0].Classes[0].Vdevs[0].Children[0].CksumErr = "0"
	m.ackRepair()
	if len(m.ackList) != 0 || m.ackPop {
		t.Fatalf("cleared line survived the refetch: %d entries", len(m.ackList))
	}
}

func TestClearFailure(t *testing.T) {
	m, _ := clearFixture()
	m.OpenAckPopup()
	m.ackKeys("enter")
	m.applyPoolClear(poolClearMsg{host: "h", pool: "p", vdev: "sda",
		text: "cannot clear errors for sda: pool I/O is currently suspended\n",
		err:  errors.New("exit status 1")})
	e := m.ackList[0]
	if e.clearSt != clearFailed || !strings.Contains(e.errText, "suspended") {
		t.Fatalf("failure not recorded: %+v", e)
	}
	// failed lines survive repair even if counters vanish — the operator
	// deserves the outcome
	m.hosts[0].pools[0].Classes[0].Vdevs[0].Children[0].CksumErr = "0"
	m.ackRepair()
	if len(m.ackList) != 1 {
		t.Fatal("failed clear line was dropped")
	}
}

func TestClearGuards(t *testing.T) {
	m, h := clearFixture()

	// no sudo: the line shows but enter refuses
	h.sudoOK = false
	m.OpenAckPopup()
	if cmd := m.ackKeys("enter"); cmd != nil || m.ackList[0].clearSt != clearIdle {
		t.Fatal("enter ran a clear without sudo")
	}
	if got := clearCmd(m.ackList[0]); got != "zpool clear p sda" {
		t.Fatalf("no-sudo cmd = %q, want bare form", got)
	}

	// interior vdev counters become ONE pool-wide line
	h.sudoOK = true
	h.pools[0].Classes[0].Vdevs[0].CksumErr = "2"
	h.pools[0].Classes[0].Vdevs[0].Children[0].CksumErr = "0"
	m.OpenAckPopup()
	if len(m.ackList) != 1 || m.ackList[0].vdev != "" {
		t.Fatalf("interior counters: %+v, want one pool-wide line", m.ackList)
	}
	if got := clearCmd(m.ackList[0]); got != "sudo -n zpool clear p" {
		t.Fatalf("pool-wide cmd = %q", got)
	}
}
