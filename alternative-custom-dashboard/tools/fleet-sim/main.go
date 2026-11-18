// fleet-sim pretends to be a set of laptops so the dashboard can be tried without real hardware.
// It registers a few fake devices across the temples the backend knows about, fills in the last
// half hour of history, then keeps sending a reading every 15 seconds.
//
//	go run ./tools/fleet-sim --server http://localhost:8080 [--token ...]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

type sample struct {
	TS        int64   `json:"ts"`
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	NetUp     float64 `json:"net_up"`
	NetDown   float64 `json:"net_down"`
	NetErr    float64 `json:"net_err"`
	LatencyMs float64 `json:"latency_ms"`
}

type profile struct {
	name                          string
	cpu, mem, up, down, errs, lat float64
	silent                        bool // registers but never reports, so it shows as offline
}

var profiles = []profile{
	{name: "Main hall laptop 1", cpu: 28, mem: 52, up: 5.2e6, down: 1.1e6, lat: 55},
	{name: "Main hall laptop 2", cpu: 33, mem: 58, up: 5.0e6, down: 1.3e6, lat: 70},
	{name: "Gate camera laptop", cpu: 96, mem: 71, up: 4.1e6, down: 0.9e6, lat: 90},      // CPU maxed out
	{name: "Aarti laptop", cpu: 12, mem: 96, up: 0.4e6, down: 0.6e6, lat: 60},            // memory full
	{name: "Ghat laptop", cpu: 40, mem: 60, up: 1.2e6, down: 0.8e6, errs: 35, lat: 2600}, // flaky network
	{name: "Spare laptop", silent: true},
}

var (
	server = flag.String("server", "http://localhost:8080", "backend URL")
	token  = flag.String("token", "", "agent token, if the backend needs one")
)

func call(method, path string, body any) []byte {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, *server+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("cannot reach %s: %v", *server, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data
}

func wobble(base, pct, t float64, seed int) float64 {
	return math.Max(0, base*(1+pct*math.Sin(t/90+float64(seed))+pct/3*(rand.Float64()-0.5)))
}

func reading(p profile, ts int64, seed int) sample {
	t := float64(ts)
	return sample{TS: ts,
		CPU: math.Min(100, wobble(p.cpu, 0.04, t, seed)), Mem: math.Min(100, wobble(p.mem, 0.03, t, seed)),
		NetUp: wobble(p.up, 0.2, t, seed), NetDown: wobble(p.down, 0.3, t, seed),
		NetErr: wobble(p.errs, 0.3, t, seed), LatencyMs: wobble(p.lat, 0.25, t, seed)}
}

func main() {
	flag.Parse()
	var temples struct {
		Temples []struct {
			ID string `json:"id"`
		} `json:"temples"`
	}
	if err := json.Unmarshal(call("GET", "/api/temples", nil), &temples); err != nil || len(temples.Temples) == 0 {
		log.Fatal("the backend has no temples to attach devices to")
	}

	ids := make([]string, len(profiles))
	for i, p := range profiles {
		temple := temples.Temples[i%len(temples.Temples)].ID
		var out struct {
			Device struct {
				ID string `json:"id"`
			} `json:"device"`
		}
		json.Unmarshal(call("POST", "/api/devices/register", map[string]any{
			"machine_id": "sim-" + p.name, "name": p.name, "hostname": strings.ReplaceAll(strings.ToLower(p.name), " ", "-"),
			"temple_id": temple, "os": "windows",
			"specs": map[string]any{"processor": "Intel Core i7-12700H", "cores": 20, "ram_gb": 16, "os_version": "Windows 11 Pro"},
		}), &out)
		ids[i] = out.Device.ID
		if p.silent {
			fmt.Printf("%-20s %s (never reports)\n", p.name, ids[i])
			continue
		}
		// the last 30 minutes, in one batch, so the charts have something in them
		var batch []sample
		now := time.Now().Unix()
		for ts := now - 30*60; ts <= now; ts += 15 {
			batch = append(batch, reading(p, ts, i))
		}
		call("POST", "/api/devices/"+ids[i]+"/samples", map[string]any{"samples": batch})
		fmt.Printf("%-20s %s\n", p.name, ids[i])
	}

	fmt.Println("sending a reading every 15 seconds; Ctrl+C to stop")
	for range time.Tick(15 * time.Second) {
		for i, p := range profiles {
			if !p.silent {
				call("POST", "/api/devices/"+ids[i]+"/samples", map[string]any{"samples": []sample{reading(p, time.Now().Unix(), i)}})
			}
		}
	}
}
