package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/ui"
	"github.com/martona/zfs-explorer/internal/zfs"
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var replays, hostFlags multiFlag
	flag.Var(&replays, "replay", "fixture `dir` (or name=dir, repeatable) instead of live collection")
	flag.Var(&hostFlags, "host", "ssh `destination` of a remote host (repeatable; adds to the hosts file)")
	noDedupe := flag.Bool("no-dedupe", false, "keep entries matching the local hostname as ssh hosts (testing)")
	dump := flag.Bool("dump", false, "render one frame to stdout and exit (no TTY needed)")
	width := flag.Int("width", 110, "frame width for --dump")
	height := flag.Int("height", 30, "frame height for --dump")
	sel := flag.String("select", "", "row to select for --dump: overview, [host:]pool, [host:]dataset, or a host name")
	browse := flag.String("browse", "", "dataset ([host:]path) whose browser level to render for --dump; a bare host name opens its host level")
	cursor := flag.String("cursor", "", "row to select within --browse (child base name or @snap)")
	expand := flag.String("expand", "", "comma-separated [host:]pools/datasets to unfold for --dump tree views")
	mark := flag.String("mark", "", "comma-separated snapshot names to select within --browse (dump)")
	perf := flag.String("perf", "", "pool ([host:]pool) whose performance screen to render for --dump")
	flag.Parse()

	specs, multi := resolveHosts(replays, hostFlags, *noDedupe)
	m := ui.New(specs, multi)

	// hostFor resolves an optional "host:" prefix on dump arguments,
	// defaulting to the first host. Pool and dataset names cannot contain
	// a colon, so the split is unambiguous; a bare host name means the
	// host itself.
	hostFor := func(arg string) (string, string) {
		for _, s := range specs {
			if s.Name == arg {
				return s.Name, ""
			}
		}
		if i := strings.IndexByte(arg, ':'); i > 0 {
			name := arg[:i]
			for _, s := range specs {
				if s.Name == name {
					return name, arg[i+1:]
				}
			}
		}
		return specs[0].Name, arg
	}
	srcOf := func(host string) collect.Source {
		for _, s := range specs {
			if s.Name == host {
				return s.Src
			}
		}
		return specs[0].Src
	}

	if *dump {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, s := range specs {
			status, list, err := s.Src.PoolTexts(ctx)
			if err != nil {
				if !multi {
					fail("collect pools: " + err.Error())
				}
				continue // a dead fleet member still renders as a row
			}
			pools := zfs.ParseZpoolStatus(status)
			zfs.AttachListNumbers(pools, list)
			m.ApplyPoolData(s.Name, pools)
			roots, _ := s.Src.RootTexts(ctx)
			poolProps, _ := s.Src.PoolProps(ctx)
			m.ApplyAuxPools(s.Name, roots, poolProps)
			if arc, iostat, objsets, err := s.Src.StatTexts(ctx); err == nil {
				m.ApplyStatData(s.Name, arc, iostat, objsets)
			}
			up, load, stat, hwmon := s.Src.HostTexts(ctx)
			m.ApplyHostVitals(s.Name, up, load, stat, hwmon)
			ver, kernel, osrel := s.Src.InfoTexts(ctx)
			m.ApplyHostInfo(s.Name, ver, kernel, osrel)
			m.MarkHostLive(s.Name)
		}
		if !strings.HasPrefix(specs[0].Src.Name(), "replay") {
			// second sample so rate-based readouts are real, not blank
			time.Sleep(1200 * time.Millisecond)
			for _, s := range specs {
				if arc, iostat, objsets, err := s.Src.StatTexts(ctx); err == nil {
					m.ApplyStatData(s.Name, arc, iostat, objsets)
					up, load, stat, hwmon := s.Src.HostTexts(ctx)
					m.ApplyHostVitals(s.Name, up, load, stat, hwmon)
				}
			}
		}
		if *expand != "" {
			fetched := map[string]bool{}
			for _, item := range strings.Split(*expand, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				host, path := hostFor(item)
				pool := strings.SplitN(path, "/", 2)[0]
				key := host + "\x00" + pool
				if !fetched[key] {
					if dsText, err := srcOf(host).DatasetTexts(ctx, pool); err == nil {
						m.ApplyDatasets(host, pool, dsText)
					}
					fetched[key] = true
				}
				m.ExpandFor(host, path)
			}
		}
		if *sel != "" {
			host, name := hostFor(*sel)
			m.SetSelected(host, name)
			if strings.Contains(name, "/") {
				if snapText, err := srcOf(host).SnapshotTexts(ctx, name); err == nil {
					m.ApplySnaps(host, name, snapText)
				}
				if propText, err := srcOf(host).PropTexts(ctx, name); err == nil {
					m.ApplyProps(host, name, propText)
				}
			}
		}
		if *browse != "" {
			host, path := hostFor(*browse)
			if path == "" { // bare host name: its pools level
				if !m.BrowseTo(host, "") {
					fail("host not found: " + *browse)
				}
			} else {
				src := srcOf(host)
				pool := strings.SplitN(path, "/", 2)[0]
				dsText, err := src.DatasetTexts(ctx, pool)
				if err != nil {
					fail("collect datasets: " + err.Error())
				}
				m.ApplyDatasets(host, pool, dsText)
				if !m.BrowseTo(host, path) {
					fail("dataset not found: " + path)
				}
				if snapText, err := src.SnapshotTexts(ctx, path); err == nil {
					m.ApplySnaps(host, path, snapText)
				}
				if propText, err := src.PropTexts(ctx, path); err == nil {
					m.ApplyProps(host, path, propText)
				}
				if *mark != "" {
					m.MarkSnaps(strings.Split(*mark, ","))
					if target := m.SelectionTarget(); target != "" {
						text, err := src.DestroyDryRun(ctx, target)
						m.ApplyDryRun(host, target, text, err)
					}
				}
				if *cursor != "" {
					if !m.SetCursorRow(*cursor) {
						fail("row not found at this level: " + *cursor)
					}
					if target := m.SelectedFamTarget(); target != "" {
						text, err := src.DestroyDryRun(ctx, target)
						m.ApplyDryRun(host, target, text, err)
					}
					if ds := m.SelectedDatasetName(); ds != "" && ds != path {
						if snapText, err := src.SnapshotTexts(ctx, ds); err == nil {
							m.ApplySnaps(host, ds, snapText)
						}
						if propText, err := src.PropTexts(ctx, ds); err == nil {
							m.ApplyProps(host, ds, propText)
						}
					}
				}
			}
		}
		if *perf != "" {
			host, pool := hostFor(*perf)
			if !m.EnterPerfFor(host, pool) {
				fail("pool not found: " + *perf)
			}
			src := srcOf(host)
			txgs, dmuTx, zil, params, iostat, err := src.PerfTexts(ctx, pool)
			m.ApplyPerf(host, pool, txgs, dmuTx, zil, params, iostat, err)
			if !strings.HasPrefix(src.Name(), "replay") {
				time.Sleep(1200 * time.Millisecond)
				txgs, dmuTx, zil, params, iostat, err = src.PerfTexts(ctx, pool)
				m.ApplyPerf(host, pool, txgs, dmuTx, zil, params, iostat, err)
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

// resolveHosts turns flags and the hosts file into the fleet: which hosts,
// in what order, served by which collector. The rules, as designed:
//   - no remotes registered → exactly the single-host tool of old
//   - any remote → host rows appear; the local host (when it has zfs)
//     joins the fleet
//   - a registered name whose first label matches the local hostname is
//     served locally instead of over ssh (dedupe — the hosts file is
//     yadm-shared and must not grow ghosts), unless --no-dedupe
func resolveHosts(replays, hostFlags multiFlag, noDedupe bool) ([]ui.HostSpec, bool) {
	var bare []string
	var named []ui.HostSpec
	for _, r := range replays {
		if i := strings.IndexByte(r, '='); i > 0 {
			named = append(named, ui.HostSpec{Name: r[:i], Dest: "replay",
				Src: collect.Replay{Dir: r[i+1:]}})
		} else {
			bare = append(bare, r)
		}
	}
	// a bare --replay dir is the classic single-host fixture mode, exclusive;
	// name=dir hosts are virtual fleet members and may ride along with live
	// ones — puppets whose every surface is a text file, re-read each tick
	if len(bare) > 0 {
		if len(bare) > 1 || len(named) > 0 || len(hostFlags) > 0 {
			fail("--replay: a bare dir is single-host; use name=dir entries to build or join a fleet")
		}
		return []ui.HostSpec{{Name: "replay", Src: collect.Replay{Dir: bare[0]}}}, false
	}

	entries := append(readHostsFile(), hostFlags...)
	localOK := runtime.GOOS == "linux" && haveZpool()
	localName := shortHostname()

	if len(entries) == 0 && len(named) > 0 && !localOK {
		// pure virtual fleet — fine anywhere
		return uniquifyNames(named), true
	}
	if len(entries) == 0 && len(named) == 0 {
		if runtime.GOOS != "linux" {
			fail("live mode needs a Linux host with ZFS; use --replay <fixture-dir> or register remote hosts")
		}
		if !haveZpool() {
			fail("zpool not found in PATH; use --replay <fixture-dir>, register remote hosts, or run on a ZFS host")
		}
		name := localName
		if name == "" {
			name = "local"
		}
		return []ui.HostSpec{{Name: name, Src: collect.Exec{}}}, false
	}

	var specs []ui.HostSpec
	localUsed := false
	for _, e := range entries {
		label := firstLabel(e)
		if !noDedupe && localOK && !localUsed && localName != "" && strings.EqualFold(label, localName) {
			specs = append(specs, ui.HostSpec{Name: label, Src: collect.Exec{}})
			localUsed = true
			continue
		}
		specs = append(specs, ui.HostSpec{Name: label, Dest: e, Src: collect.Ssh{Dest: e, Label: label}})
	}
	if !localUsed && localOK {
		name := localName
		if name == "" {
			name = "local"
		}
		specs = append([]ui.HostSpec{{Name: name, Src: collect.Exec{}}}, specs...)
	}
	specs = append(specs, named...)
	if len(specs) == 0 {
		fail("no usable hosts: local has no zfs and no remotes resolved")
	}
	return uniquifyNames(specs), true
}

// readHostsFile loads ~/.config/zfse/hosts: one ssh destination per line,
// #-comments, blank lines ignored. Absent file = no entries.
func readHostsFile() []string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "zfse", "hosts"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// firstLabel reduces an ssh destination to its display name: strip user@,
// keep the first hostname label.
func firstLabel(dest string) string {
	host := dest
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return host
}

func shortHostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return firstLabel(name)
}

func haveZpool() bool {
	_, err := exec.LookPath("zpool")
	return err == nil
}

// uniquifyNames disambiguates colliding display names — deliberate with
// --no-dedupe self-registration, accidental otherwise; either way the tree
// needs distinct ids.
func uniquifyNames(specs []ui.HostSpec) []ui.HostSpec {
	seen := map[string]int{}
	for i := range specs {
		seen[specs[i].Name]++
		if n := seen[specs[i].Name]; n > 1 {
			specs[i].Name = fmt.Sprintf("%s(%d)", specs[i].Name, n)
		}
	}
	return specs
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "zfse: "+msg)
	os.Exit(1)
}
