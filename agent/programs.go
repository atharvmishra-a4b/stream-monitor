package main

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// The top programs by CPU and by memory, so a red laptop can say why.
// Programs are grouped by name (all of Chrome's processes count as "chrome.exe") and only names are
// reported, never window titles, files or command lines.

const (
	topN   = 5
	minCPU = 0.05 // percent of the whole laptop; below this a program is not "using CPU"
)

// ProgramUse is a program's share of the whole laptop, in percent.
type ProgramUse struct {
	Name string
	CPU  float64
	Mem  float64
}

type procStat struct {
	pid    int32
	name   string
	cpuSec float64 // total CPU time used so far
	hasCPU bool
	rss    uint64
}

func listProcesses(ctx context.Context) ([]procStat, error) {
	if runtime.GOOS == "darwin" {
		// one ps call instead of several per process, which would be slow and add load of its own
		if ps, err := listProcessesPS(ctx); err == nil && len(ps) > 0 {
			return ps, nil
		}
	}
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]procStat, 0, len(procs))
	for _, p := range procs {
		if p.Pid == 0 { // the idle "process" is not a program
			continue
		}
		name, err := p.NameWithContext(ctx)
		if err != nil || name == "" {
			continue
		}
		ps := procStat{pid: p.Pid, name: name}
		if t, err := p.TimesWithContext(ctx); err == nil {
			ps.cpuSec, ps.hasCPU = t.User+t.System, true
		}
		if mi, err := p.MemoryInfoWithContext(ctx); err == nil && mi != nil {
			ps.rss = mi.RSS
		}
		out = append(out, ps)
	}
	return out, nil
}

func listProcessesPS(ctx context.Context) ([]procStat, error) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,rss=,time=,comm=").Output()
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

func parsePS(b []byte) []procStat {
	var out []procStat
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		rssKB, e2 := strconv.ParseUint(f[1], 10, 64)
		cpu, ok := parsePSTime(f[2])
		if e1 != nil || e2 != nil || !ok || pid == 0 {
			continue
		}
		// the program path is the rest of the line and can contain spaces
		out = append(out, procStat{pid: int32(pid), name: filepath.Base(strings.Join(f[3:], " ")), cpuSec: cpu, hasCPU: true, rss: rssKB * 1024})
	}
	return out
}

// parsePSTime reads ps CPU times: "0:01.23", "12:34.56", "1:02:03" or "2-03:04:05".
func parsePSTime(s string) (float64, bool) {
	days := 0.0
	if i := strings.Index(s, "-"); i >= 0 {
		d, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + v
	}
	return days*86400 + total, true
}

// readPrograms measures every program's CPU since the previous call and its share of memory.
func (a *agent) readPrograms(now time.Time) []ProgramUse {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	procs, err := listProcesses(ctx)
	if err != nil {
		return nil
	}
	totalMem := 0.0
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm != nil {
		totalMem = float64(vm.Total)
	}
	dt := now.Sub(a.prevProcTime).Seconds()
	ncpu := float64(runtime.NumCPU())
	cur := make(map[int32]float64, len(procs))
	byName := map[string]*ProgramUse{}
	for _, p := range procs {
		u := byName[p.name]
		if u == nil {
			u = &ProgramUse{Name: truncate(p.name, 60)}
			byName[p.name] = u
		}
		if p.hasCPU {
			cur[p.pid] = p.cpuSec
			if prev, ok := a.prevProcCPU[p.pid]; ok && dt > 0 && p.cpuSec >= prev {
				u.CPU += (p.cpuSec - prev) / dt / ncpu * 100 // share of the whole laptop, like the overall CPU figure
			}
		}
		if totalMem > 0 {
			u.Mem += float64(p.rss) / totalMem * 100
		}
	}
	a.prevProcCPU, a.prevProcTime = cur, now
	out := make([]ProgramUse, 0, len(byName))
	for _, u := range byName {
		u.CPU, u.Mem = minf(u.CPU, 100), minf(u.Mem, 100)
		out = append(out, *u)
	}
	return out
}

// selectTop keeps the top programs by CPU and by memory, and lists the ones that have dropped out since last time
// (the caller tells Prometheus those series have ended).
func (a *agent) selectTop(all []ProgramUse) (keep []ProgramUse, gone []string) {
	chosen := map[string]ProgramUse{}
	byCPU := append([]ProgramUse(nil), all...)
	sort.Slice(byCPU, func(i, j int) bool { return byCPU[i].CPU > byCPU[j].CPU })
	for i := 0; i < len(byCPU) && i < topN && byCPU[i].CPU >= minCPU; i++ {
		chosen[byCPU[i].Name] = byCPU[i]
	}
	byMem := append([]ProgramUse(nil), all...)
	sort.Slice(byMem, func(i, j int) bool { return byMem[i].Mem > byMem[j].Mem })
	for i := 0; i < len(byMem) && i < topN; i++ {
		chosen[byMem[i].Name] = byMem[i]
	}
	for name := range a.lastProgSet {
		if _, still := chosen[name]; !still {
			gone = append(gone, name)
		}
	}
	a.lastProgSet = make(map[string]bool, len(chosen))
	for name, u := range chosen {
		a.lastProgSet[name] = true
		keep = append(keep, u)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].Name < keep[j].Name })
	sort.Strings(gone)
	return keep, gone
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
