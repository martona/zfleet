package zfs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// zpool status has no machine-readable mode in zfs 2.2.x, so this parser is
// written against captured fixtures (testdata/fixtures/). Layout facts it
// relies on:
//   - section keys (pool/state/status/action/see/scan/config/errors) start a
//     line, possibly space-indented, and are followed by ":"
//   - free-text sections wrap with tab-indented continuation lines
//   - inside config:, device rows start with a tab; nesting is two spaces per
//     level after that tab
//   - allocation-class labels (special, logs, cache, ...) appear inside
//     config at column 0, no leading tab
var statusSectionRe = regexp.MustCompile(`^\s*(pool|state|status|action|see|scan|config|errors):\s?(.*)$`)

var classLabels = map[string]bool{
	"special": true,
	"logs":    true,
	"cache":   true,
	"spares":  true,
	"dedup":   true,
}

// ParseZpoolStatus parses the full multi-pool output of `zpool status`.
func ParseZpoolStatus(text string) []*Pool {
	var pools []*Pool
	var p *Pool
	var section string
	var scanLines []string
	var class *VdevClass
	inConfig := false
	// stack[i] is the most recent vdev seen at depth i+1 (depth 1 = top-level
	// vdev). Nodes are heap-allocated so stored pointers stay valid as
	// sibling slices grow.
	var stack []*Vdev

	finishScan := func() {
		if p != nil && len(scanLines) > 0 {
			p.Scan = parseScan(scanLines)
			scanLines = nil
		}
	}

	newClass := func(name string) {
		class = &VdevClass{Name: name, Size: -1, Alloc: -1}
		p.Classes = append(p.Classes, class)
		stack = nil
	}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		if m := statusSectionRe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "\t") {
			section = m[1]
			rest := m[2]
			switch section {
			case "pool":
				finishScan()
				p = &Pool{Name: strings.TrimSpace(rest), Size: -1, Alloc: -1, Free: -1, FragPct: -1, CapPct: -1}
				pools = append(pools, p)
				class = nil
				inConfig = false
			case "state":
				if p != nil {
					p.State = strings.TrimSpace(rest)
				}
			case "status", "action", "see":
				if p != nil && strings.TrimSpace(rest) != "" {
					p.Notes = append(p.Notes, section+": "+strings.TrimSpace(rest))
				}
			case "scan":
				scanLines = append(scanLines, strings.TrimSpace(rest))
			case "config":
				finishScan()
				inConfig = true
			case "errors":
				inConfig = false
				if p != nil {
					p.ErrorsLine = strings.TrimSpace(rest)
				}
			}
			continue
		}

		if p == nil {
			continue
		}

		if !inConfig {
			// tab-indented continuation of the current free-text section
			t := strings.TrimSpace(line)
			switch section {
			case "scan":
				scanLines = append(scanLines, t)
			case "status", "action", "see":
				// wrapped continuation of the previous note line
				if len(p.Notes) > 0 {
					p.Notes[len(p.Notes)-1] += " " + t
				}
			}
			continue
		}

		// inside config:
		if !strings.HasPrefix(line, "\t") {
			// column-0 word inside config = allocation class label
			label := strings.ToLower(strings.TrimSpace(line))
			if classLabels[label] {
				newClass(label)
			}
			continue
		}

		content := line[1:]
		depth := 0
		for strings.HasPrefix(content, "  ") {
			content = content[2:]
			depth++
		}
		fields := strings.Fields(content)
		if len(fields) == 0 || (fields[0] == "NAME" && strings.Contains(content, "STATE")) {
			continue
		}

		if depth == 0 {
			// either the pool's own row, or an allocation-class label —
			// fixtures show labels as a lone tab-prefixed word ("\tspecial")
			if len(fields) == 1 && classLabels[strings.ToLower(fields[0])] {
				newClass(strings.ToLower(fields[0]))
			}
			continue
		}

		v := &Vdev{Name: fields[0], Size: -1, Alloc: -1, Free: -1}
		if len(fields) > 1 {
			v.State = fields[1]
		}
		if len(fields) >= 5 {
			v.ReadErr, v.WriteErr, v.CksumErr = fields[2], fields[3], fields[4]
			if len(fields) > 5 {
				v.Note = strings.Join(fields[5:], " ")
			}
		} else if len(fields) > 2 {
			v.Note = strings.Join(fields[2:], " ")
		}

		if depth == 1 {
			if class == nil {
				newClass("data")
			}
			class.Vdevs = append(class.Vdevs, v)
			stack = stack[:0]
			stack = append(stack, v)
		} else {
			if depth-1 <= len(stack) && depth >= 2 {
				parent := stack[depth-2]
				parent.Children = append(parent.Children, v)
				stack = stack[:depth-1]
				stack = append(stack, v)
			}
			// rows deeper than anything we've tracked are silently skipped;
			// fixtures say this doesn't happen
		}
	}
	finishScan()
	return pools
}

var (
	scanDoneRe     = regexp.MustCompile(`(scrub repaired|resilvered) (\S+) in (.+?) with (\d+) errors on (.+)$`)
	scanProgressRe = regexp.MustCompile(`(scrub|resilver) in progress since (.+)$`)
	scanPausedRe   = regexp.MustCompile(`scrub paused since (.+)$`)
	pctDoneRe      = regexp.MustCompile(`([\d.]+)% done`)
	toGoRe         = regexp.MustCompile(`, ([^,]+) to go`)
)

func parseScan(lines []string) Scan {
	s := Scan{Raw: lines}
	head := lines[0]

	if m := scanDoneRe.FindStringSubmatch(head); m != nil {
		s.State = ScanDone
		s.Kind = "scrub"
		if strings.HasPrefix(m[1], "resilver") {
			s.Kind = "resilver"
		}
		s.Errors, _ = strconv.ParseInt(m[4], 10, 64)
		s.Summary = fmt.Sprintf("ok %s · %s · repaired %s",
			NiceCtimeDate(m[5]), NiceClockDur(m[3]), m[2])
		if s.Errors > 0 {
			s.Summary = fmt.Sprintf("%d errors · %s · %s", s.Errors, NiceCtimeDate(m[5]), NiceClockDur(m[3]))
		}
		return s
	}

	paused := scanPausedRe.MatchString(head)
	if m := scanProgressRe.FindStringSubmatch(head); m != nil || paused {
		s.State = ScanInProgress
		s.Kind = "scrub"
		if m != nil {
			s.Kind = m[1]
		}
		rest := strings.Join(lines[1:], " ")
		parts := []string{}
		if pm := pctDoneRe.FindStringSubmatch(rest); pm != nil {
			s.Percent, _ = strconv.ParseFloat(pm[1], 64)
			parts = append(parts, fmt.Sprintf("%.1f%% done", s.Percent))
		} else {
			parts = append(parts, s.Kind+" running")
		}
		if tm := toGoRe.FindStringSubmatch(rest); tm != nil {
			parts = append(parts, "~"+NiceClockDur(tm[1])+" to go")
		}
		if paused {
			parts = append(parts, "paused")
		}
		s.Summary = strings.Join(parts, " · ")
		return s
	}

	s.State = ScanNone
	s.Summary = head
	return s
}
