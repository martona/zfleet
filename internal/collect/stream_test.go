package collect

import (
	"io"
	"strings"
	"testing"
	"time"
)

// Framing pins, all cp4-verified behaviors: -y prints the suppressed boot
// sample's timestamp (consecutive stamps → empty block, dropped), blocks
// are delimited by the next stamp or EOF, and replay text with no stamps at
// all is one block.
func TestScanIostatBlocks(t *testing.T) {
	text := "1785431440\n1785431442\na\t1\nb\t2\n1785431444\nc\t3\n"
	var blocks []string
	if err := ScanIostatBlocks(strings.NewReader(text), func(b string) bool {
		blocks = append(blocks, b)
		return true
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(blocks) != 2 || blocks[0] != "a\t1\nb\t2" || blocks[1] != "c\t3" {
		t.Fatalf("blocks = %q, want [a/b, c]", blocks)
	}

	// emit returning false stops the scan
	blocks = nil
	ScanIostatBlocks(strings.NewReader(text), func(b string) bool {
		blocks = append(blocks, b)
		return false
	})
	if len(blocks) != 1 {
		t.Fatalf("early stop delivered %d blocks, want 1", len(blocks))
	}

	// no timestamps at all (replay fixtures): one block at EOF
	blocks = nil
	ScanIostatBlocks(strings.NewReader("x\t1\ny\t2\n"), func(b string) bool {
		blocks = append(blocks, b)
		return true
	})
	if len(blocks) != 1 || blocks[0] != "x\t1\ny\t2" {
		t.Fatalf("unterminated block = %q", blocks)
	}
}

// A block must be delivered on row-silence, not held hostage until the next
// interval's timestamp 2s later — the whole point of streaming is losing
// the staleness.
func TestScanIostatIdleFlush(t *testing.T) {
	pr, pw := io.Pipe()
	firstAt := make(chan time.Duration, 4)
	start := time.Now()
	go func() {
		pw.Write([]byte("1785431442\na\t1\nb\t2\n"))
		time.Sleep(4 * blockIdleFlush)
		pw.Write([]byte("1785431444\nc\t3\n"))
		pw.Close()
	}()
	ScanIostatBlocks(pr, func(b string) bool {
		firstAt <- time.Since(start)
		return true
	})
	if got := <-firstAt; got >= 4*blockIdleFlush {
		t.Fatalf("first block arrived at %v — only with the next timestamp, idle flush is dead", got)
	}
	if n := len(firstAt); n != 1 {
		t.Fatalf("expected exactly one more block pending, have %d extra", n)
	}
}
