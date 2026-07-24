package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/ui"
	"github.com/martona/zfs-explorer/internal/zfs"
)

func main() {
	replay := flag.String("replay", "", "run against recorded fixtures in `dir` instead of live zpool commands")
	dump := flag.Bool("dump", false, "render one frame to stdout and exit (no TTY needed)")
	width := flag.Int("width", 110, "frame width for --dump")
	height := flag.Int("height", 30, "frame height for --dump")
	sel := flag.String("select", "", "pool to select for --dump")
	flag.Parse()

	var src collect.Source
	if *replay != "" {
		src = collect.Replay{Dir: *replay}
	} else {
		if runtime.GOOS != "linux" {
			fail("live mode needs a Linux host with ZFS; use --replay <fixture-dir> elsewhere")
		}
		if _, err := exec.LookPath("zpool"); err != nil {
			fail("zpool not found in PATH; use --replay <fixture-dir> or run on a ZFS host")
		}
		src = collect.Exec{}
	}

	m := ui.New(src)

	if *dump {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		status, list, err := src.PoolTexts(ctx)
		if err != nil {
			fail("collect pools: " + err.Error())
		}
		pools := zfs.ParseZpoolStatus(status)
		zfs.AttachListNumbers(pools, list)
		m.ApplyPoolData(pools)
		arc, iostat, err := src.StatTexts(ctx)
		if err == nil {
			m.ApplyStatData(arc, iostat)
		}
		if *sel != "" {
			m.SetSelected(*sel)
		}
		m.SetSize(*width, *height)
		fmt.Println(m.View())
		return
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "zfse: "+msg)
	os.Exit(1)
}
