package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfleet/internal/collect"
)

// The iostat stream: one long-lived `zpool iostat -Hpvly -T u 2` per host,
// spawned at startup, feeding every consumer — strip and tree pool
// bandwidth, the pool panel's vdev latency rings — from the same
// kernel-timed 2s samples. A stalled kernel sample arrives late but nothing
// piles up: there is no per-tick fork to stack, and every op on the host
// lands in some window instead of the one-shot era's ~1s-in-2 blind spots.
//
// Plumbing: each host's reader is a single tea.Cmd goroutine that scans the
// stream into blocks and pushes them into the model's shared channel; one
// listener Cmd drains that channel, re-armed on every delivery. The
// reader's own return value is its death notice, which schedules a respawn.

const streamRetry = 5 * time.Second

type iostatBlockMsg struct {
	host string
	text string
}
type iostatDownMsg struct {
	host string
	err  error
}
type iostatRetryMsg struct{ host string }

// listenIostat delivers the next stream block from any host's reader.
// Exactly one listener is armed at a time: Init starts it, and only a
// delivered block re-arms it.
func (m *Model) listenIostat() tea.Cmd {
	ch := m.streamCh
	return func() tea.Msg { return <-ch }
}

// readIostat opens a host's stream and pumps blocks until it dies. The
// blocks go through the channel; the returned message is the death notice.
func readIostat(h *hostState, ch chan<- tea.Msg) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		rc, err := src.IostatStream(context.Background())
		if err != nil {
			return iostatDownMsg{host: host, err: err}
		}
		defer rc.Close()
		err = collect.ScanIostatBlocks(rc, func(block string) bool {
			ch <- iostatBlockMsg{host: host, text: block}
			return true
		})
		return iostatDownMsg{host: host, err: err}
	}
}
