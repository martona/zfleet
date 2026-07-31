package collect

import (
	"bufio"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/martona/zfleet/internal/zfs"
)

// streamCmd starts a long-lived command and hands back its stdout as a
// ReadCloser. Close kills the process and reaps it; on Linux Pdeathsig
// additionally guarantees the child dies with us, so a crashed zfleet can
// never orphan a stream.
type procReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (p *procReader) Close() error {
	p.ReadCloser.Close()
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	return p.cmd.Wait()
}

func streamCmd(cmd *exec.Cmd) (io.ReadCloser, error) {
	cmd.SysProcAttr = streamProcAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procReader{ReadCloser: stdout, cmd: cmd}, nil
}

// A block's rows arrive together (iostat flushes each interval into the
// pipe, milliseconds apart), but its closing `-T u` timestamp only comes
// with the NEXT interval 2s later — so a short silence after rows is the
// real end-of-block signal, and waiting for the delimiter would cost every
// sample one interval of staleness.
const blockIdleFlush = 300 * time.Millisecond

// ScanIostatBlocks splits a `zpool iostat -Hpvly -T u` stream into sample
// blocks and hands each to emit as soon as it is complete — on the next
// timestamp line, on row-silence, or at EOF. Consecutive timestamps (the
// -y quirk: a suppressed boot sample still prints its stamp) yield empty
// blocks, which are dropped. Returning false from emit stops the scan.
func ScanIostatBlocks(r io.Reader, emit func(block string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lines := make(chan string, 64)
	errc := make(chan error, 1)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
		errc <- sc.Err()
		close(lines)
	}()
	var cur []string
	flush := func() bool {
		if len(cur) == 0 {
			return true
		}
		b := strings.Join(cur, "\n")
		cur = nil
		return emit(b)
	}
	idle := time.NewTimer(time.Hour)
	defer idle.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				flush() // replay fixtures end in an unterminated block
				return <-errc
			}
			line = strings.TrimRight(line, "\r")
			switch {
			case zfs.IostatTimestamp(line):
				if !flush() {
					return nil
				}
			case line != "":
				cur = append(cur, line)
				idle.Reset(blockIdleFlush)
			}
		case <-idle.C:
			if !flush() {
				return nil
			}
		}
	}
}
