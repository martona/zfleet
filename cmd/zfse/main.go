package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	browse := flag.String("browse", "", "dataset whose browser level to render for --dump")
	cursor := flag.String("cursor", "", "row to select within --browse (child base name or @snap)")
	expand := flag.String("expand", "", "comma-separated pools/datasets to unfold for --dump tree views")
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
		roots, _ := src.RootTexts(ctx)
		poolProps, _ := src.PoolProps(ctx)
		m.ApplyAuxPools(roots, poolProps)
		arc, iostat, objsets, err := src.StatTexts(ctx)
		if err == nil {
			m.ApplyStatData(arc, iostat, objsets)
		}
		if !strings.HasPrefix(src.Name(), "replay") {
			// second sample so rate-based readouts are real, not blank
			time.Sleep(1200 * time.Millisecond)
			if arc, iostat, objsets, err = src.StatTexts(ctx); err == nil {
				m.ApplyStatData(arc, iostat, objsets)
			}
		}
		if *expand != "" {
			fetched := map[string]bool{}
			for _, item := range strings.Split(*expand, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				pool := strings.SplitN(item, "/", 2)[0]
				if !fetched[pool] {
					if dsText, err := src.DatasetTexts(ctx, pool); err == nil {
						m.ApplyDatasets(pool, dsText)
					}
					fetched[pool] = true
				}
				m.ExpandFor(item)
			}
		}
		if *sel != "" {
			m.SetSelected(*sel)
			if strings.Contains(*sel, "/") {
				if snapText, err := src.SnapshotTexts(ctx, *sel); err == nil {
					m.ApplySnaps(*sel, snapText)
				}
				if propText, err := src.PropTexts(ctx, *sel); err == nil {
					m.ApplyProps(*sel, propText)
				}
			}
		}
		if *browse != "" {
			pool := strings.SplitN(*browse, "/", 2)[0]
			dsText, err := src.DatasetTexts(ctx, pool)
			if err != nil {
				fail("collect datasets: " + err.Error())
			}
			m.ApplyDatasets(pool, dsText)
			if !m.BrowseTo(*browse) {
				fail("dataset not found: " + *browse)
			}
			if snapText, err := src.SnapshotTexts(ctx, *browse); err == nil {
				m.ApplySnaps(*browse, snapText)
			}
			if propText, err := src.PropTexts(ctx, *browse); err == nil {
				m.ApplyProps(*browse, propText)
			}
			if *cursor != "" {
				if !m.SetCursorRow(*cursor) {
					fail("row not found at this level: " + *cursor)
				}
				if ds := m.SelectedDatasetName(); ds != "" && ds != *browse {
					if snapText, err := src.SnapshotTexts(ctx, ds); err == nil {
						m.ApplySnaps(ds, snapText)
					}
					if propText, err := src.PropTexts(ctx, ds); err == nil {
						m.ApplyProps(ds, propText)
					}
				}
			}
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
