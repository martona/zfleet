package zfs

import "strings"

// AttachListNumbers joins `zpool list -Hpv` output onto pools parsed from
// zpool status, matching vdevs by name within each pool.
//
// Fixture-verified layout quirks of -Hpv in zfs 2.2.x:
//   - pool rows: column 0, 11 tab-separated fields
//     (name size alloc free ckpoint expandsz frag cap dedup health altroot)
//   - vdev/disk rows: one leading tab, 10 tab-separated fields, and the tree
//     is FLATTENED — disks and top-level vdevs get the same single tab
//   - class labels (special/logs/...): column 0 but space-padded, not
//     tab-separated
func AttachListNumbers(pools []*Pool, text string) {
	byName := map[string]*Pool{}
	for _, p := range pools {
		byName[p.Name] = p
	}

	var vdevs map[string]*Vdev // name -> node, scoped to the current pool
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		if !strings.HasPrefix(line, "\t") {
			f := strings.Split(line, "\t")
			if len(f) < 10 {
				// space-padded class label row; vdev names are unique within
				// a pool, so nothing to track here
				continue
			}
			p := byName[f[0]]
			if p == nil {
				vdevs = nil
				continue
			}
			p.Size = parseI64(f[1])
			p.Alloc = parseI64(f[2])
			p.Free = parseI64(f[3])
			p.FragPct = parseI64(f[6])
			p.CapPct = parseI64(f[7])
			p.Dedup = f[8]
			vdevs = map[string]*Vdev{}
			for _, c := range p.Classes {
				for _, v := range c.Vdevs {
					indexVdev(v, vdevs)
				}
			}
			continue
		}

		if vdevs == nil {
			continue
		}
		f := strings.Split(line[1:], "\t")
		if len(f) < 10 {
			continue
		}
		if v := vdevs[f[0]]; v != nil {
			v.Size = parseI64(f[1])
			v.Alloc = parseI64(f[2])
			v.Free = parseI64(f[3])
		}
	}

	for _, p := range pools {
		for _, c := range p.Classes {
			c.Size, c.Alloc = -1, -1
			for _, v := range c.Vdevs {
				if v.Size >= 0 {
					if c.Size < 0 {
						c.Size, c.Alloc = 0, 0
					}
					c.Size += v.Size
					if v.Alloc >= 0 {
						c.Alloc += v.Alloc
					}
				}
			}
		}
	}
}

func indexVdev(v *Vdev, m map[string]*Vdev) {
	if _, dup := m[v.Name]; !dup {
		m[v.Name] = v
	}
	for _, c := range v.Children {
		indexVdev(c, m)
	}
}
