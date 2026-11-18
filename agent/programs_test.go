package main

import (
	"math"
	"testing"

	"github.com/dev-a4b/stream-monitor/agent/rw"
)

func TestParsePSTime(t *testing.T) {
	cases := map[string]float64{"0:01.50": 1.5, "12:34.56": 754.56, "1:02:03": 3723, "2-03:04:05": 2*86400 + 3*3600 + 4*60 + 5, "123:45.67": 7425.67}
	for in, want := range cases {
		if got, ok := parsePSTime(in); !ok || math.Abs(got-want) > 0.001 {
			t.Errorf("%q: got %v ok=%v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "12", "a:b", "1:2:3:4"} {
		if _, ok := parsePSTime(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestParsePS(t *testing.T) {
	out := []byte("    0     0 0:00.00 kernel_task\n  412 81234 1:02.50 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome\n  99  2048 0:00.10 /usr/sbin/cfprefsd\nbroken line\n")
	got := parsePS(out)
	if len(got) != 2 {
		t.Fatalf("want 2 programs (pid 0 and the broken line skipped), got %d: %+v", len(got), got)
	}
	if got[0].name != "Google Chrome" || got[0].rss != 81234*1024 || got[0].cpuSec != 62.5 || got[0].pid != 412 {
		t.Errorf("chrome parsed wrong: %+v", got[0])
	}
	if got[1].name != "cfprefsd" {
		t.Errorf("name should be the program, not its path: %+v", got[1])
	}
}

func TestSelectTop(t *testing.T) {
	a := &agent{}
	all := []ProgramUse{
		{"obs", 30, 5}, {"chrome", 10, 20}, {"idle1", 0, 0.1}, {"a", 1, 1}, {"b", 2, 2}, {"c", 3, 3}, {"d", 4, 4}, {"e", 0, 9},
	}
	keep, gone := a.selectTop(all)
	names := map[string]bool{}
	for _, u := range keep {
		names[u.Name] = true
	}
	for _, want := range []string{"obs", "chrome", "d", "c", "b", "e"} { // top 5 by CPU plus top 5 by memory
		if !names[want] {
			t.Errorf("%s should be kept", want)
		}
	}
	if names["idle1"] || len(gone) != 0 {
		t.Errorf("idle program kept or something gone on the first report: keep=%v gone=%v", names, gone)
	}
	// next report: obs stops using CPU and leaves the lists
	keep, gone = a.selectTop([]ProgramUse{{"chrome", 10, 20}, {"e", 0, 9}})
	if len(gone) == 0 {
		t.Fatalf("programs that left the lists must be reported as gone, got %v (keep %v)", gone, keep)
	}
}

func TestProgramSeriesMarkStale(t *testing.T) {
	a := &agent{}
	base := []rw.Label{{Name: "device", Value: "x"}}
	batch := []Sample{
		{TS: 100, Programs: []ProgramUse{{"obs", 40, 5}, {"chrome", 10, 20}}},
		{TS: 160, Programs: []ProgramUse{{"chrome", 12, 20}}, Gone: []string{"obs"}},
	}
	var obsCPU []rw.Point
	for _, s := range a.programSeries(base, batch) {
		if s.Name == "stream_laptop_program_cpu_percent" && s.Labels[len(s.Labels)-1].Value == "obs" {
			obsCPU = s.Samples
		}
	}
	if len(obsCPU) != 2 || obsCPU[0].Value != 40 || !math.IsNaN(obsCPU[1].Value) || obsCPU[1].TS != 160000 {
		t.Errorf("obs should have a real reading then a stale marker at 160s, got %+v", obsCPU)
	}
	if math.Float64bits(obsCPU[1].Value) != math.Float64bits(rw.StaleNaN) {
		t.Error("the marker must be Prometheus's exact stale value, not just any NaN")
	}
}
