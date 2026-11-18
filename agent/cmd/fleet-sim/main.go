// fleet-sim pretends to be a handful of laptops so the Grafana dashboards can be tried without hardware.
// It pushes the last 45 minutes of history, then a reading every 15 seconds.
//
//	go run ./cmd/fleet-sim --remote-write http://localhost:9090/api/v1/write
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/dev-a4b/stream-monitor/agent/rw"
)

type sample struct {
	ts                            int64
	cpu, mem, up, down, errs, lat float64
}

type fake struct {
	hub, temple, name             string
	cpu, mem, up, down, errs, lat float64
	quietFor                      time.Duration // stops reporting this long ago (to show an offline laptop)
}

var fleet = []fake{
	{hub: "Ujjain", temple: "Temple A", name: "Main hall 1", cpu: 28, mem: 52, up: 5.2e6, down: 1.1e6, lat: 55},
	{hub: "Ujjain", temple: "Temple A", name: "Main hall 2", cpu: 33, mem: 58, up: 5.0e6, down: 1.3e6, lat: 70},
	{hub: "Ujjain", temple: "Temple A", name: "Gate camera", cpu: 96, mem: 71, up: 4.1e6, down: 0.9e6, lat: 90},               // CPU maxed out
	{hub: "Ujjain", temple: "Temple B", name: "Aarti laptop", cpu: 12, mem: 96, up: 0.4e6, down: 0.6e6, lat: 60},              // memory full
	{hub: "Varanasi", temple: "Temple C", name: "Ghat laptop", cpu: 40, mem: 60, up: 1.2e6, down: 0.8e6, errs: 35, lat: 2600}, // flaky network
	{hub: "Haridwar", temple: "Temple D", name: "Ganga laptop", cpu: 22, mem: 49, up: 4.8e6, down: 1.0e6, lat: 80},
	{hub: "Haridwar", temple: "Temple D", name: "Spare laptop", cpu: 10, mem: 40, up: 0.5e6, down: 0.5e6, lat: 70, quietFor: 10 * time.Minute},
}

func wobble(base, pct float64, t int64, seed int) float64 {
	return math.Max(0, base*(1+pct*math.Sin(float64(t)/90+float64(seed))+pct/3*(rand.Float64()-0.5)))
}

func reading(f fake, ts int64, seed int) sample {
	return sample{ts: ts, cpu: math.Min(100, wobble(f.cpu, 0.04, ts, seed)), mem: math.Min(100, wobble(f.mem, 0.02, ts, seed)),
		up: wobble(f.up, 0.2, ts, seed), down: wobble(f.down, 0.3, ts, seed), errs: wobble(f.errs, 0.3, ts, seed), lat: wobble(f.lat, 0.25, ts, seed)}
}

func series(f fake, batch []sample) []rw.Series {
	base := []rw.Label{{Name: "hub", Value: f.hub}, {Name: "temple", Value: f.temple}, {Name: "device", Value: f.name}}
	metrics := []struct {
		name string
		get  func(sample) float64
	}{
		{"stream_laptop_cpu_percent", func(s sample) float64 { return s.cpu }},
		{"stream_laptop_memory_percent", func(s sample) float64 { return s.mem }},
		{"stream_laptop_network_upload_bps", func(s sample) float64 { return s.up }},
		{"stream_laptop_network_download_bps", func(s sample) float64 { return s.down }},
		{"stream_laptop_network_errors_per_minute", func(s sample) float64 { return s.errs }},
		{"stream_laptop_report_latency_ms", func(s sample) float64 { return s.lat }},
	}
	var out []rw.Series
	for _, m := range metrics {
		pts := make([]rw.Point, len(batch))
		for i, s := range batch {
			pts[i] = rw.Point{TS: s.ts * 1000, Value: m.get(s)}
		}
		out = append(out, rw.Series{Name: m.name, Labels: base, Samples: pts})
	}
	info := append([]rw.Label{{Name: "os", Value: "windows"}, {Name: "processor", Value: "Intel Core i7-12700H"}, {Name: "cores", Value: "20"}, {Name: "ram_gb", Value: "16"}, {Name: "os_version", Value: "Windows 11 Pro"}}, base...)
	return append(out, rw.Series{Name: "stream_laptop_info", Labels: info, Samples: []rw.Point{{TS: batch[len(batch)-1].ts * 1000, Value: 1}}})
}

func main() {
	url := flag.String("remote-write", "http://localhost:9090/api/v1/write", "Prometheus remote-write URL")
	user := flag.String("username", "", "basic-auth user")
	pass := flag.String("password", "", "basic-auth password")
	flag.Parse()
	auth := rw.Auth{Username: *user, Password: *pass}
	client := &http.Client{Timeout: 10 * time.Second}
	push := func(f fake, b []sample) {
		if _, _, err := rw.Push(context.Background(), client, *url, auth, series(f, b)); err != nil {
			log.Printf("%s: %v", f.name, err)
		}
	}

	now := time.Now().Unix()
	for i, f := range fleet {
		var batch []sample
		end := now - int64(f.quietFor.Seconds())
		for ts := now - 45*60; ts <= end; ts += 15 {
			batch = append(batch, reading(f, ts, i))
		}
		push(f, batch)
		fmt.Printf("%-9s %-9s %s\n", f.hub, f.temple, f.name)
	}
	fmt.Println("sending a reading every 15 seconds; Ctrl+C to stop")
	for range time.Tick(15 * time.Second) {
		for i, f := range fleet {
			if f.quietFor == 0 {
				push(f, []sample{reading(f, time.Now().Unix(), i)})
			}
		}
	}
}
