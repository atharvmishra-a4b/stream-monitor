// stream-agent measures this laptop (CPU, memory, network) every few seconds and reports it.
//
// Two ways to report:
//
//	--remote-write URL   push straight to a Prometheus server (read the results in Grafana)
//	--server URL         send to the optional custom backend (see alternative-custom-dashboard/)
//
// If the connection drops it keeps the readings and sends them when it is back, so history has no gaps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dev-a4b/stream-monitor/agent/rw"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

const (
	bufferFor = 8 * time.Hour // how much history to keep while the connection is down
	maxQueued = 10000         // hard ceiling on queued readings, whatever the interval
	maxBatch  = 120           // readings per request in Prometheus mode
)

// queueCap keeps the offline buffer worth the same amount of time at any --interval,
// so a shorter interval does not silently shorten how long an outage can last.
func queueCap(interval time.Duration) int {
	if interval <= 0 {
		return maxQueued
	}
	return min(max(int(bufferFor/interval), 100), maxQueued)
}

type Sample struct {
	TS        int64   `json:"ts"`
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	NetUp     float64 `json:"net_up"`
	NetDown   float64 `json:"net_down"`
	NetErr    float64 `json:"net_err"`
	LatencyMs float64 `json:"latency_ms"`

	Programs []ProgramUse `json:"-"` // top programs, when this reading includes them
	Gone     []string     `json:"-"` // programs that dropped out of the top since the last reading
}

type agent struct {
	// Prometheus mode
	rwURL, hub, user, pass string
	// custom backend mode
	server string
	// both
	temple, name, token string
	interval            time.Duration
	client              *http.Client

	specs  map[string]any
	osName string

	deviceID    string // custom backend mode only
	queue       []Sample
	lastLatency float64
	failing     bool
	failCount   int

	prevNet  map[string]gnet.IOCountersStat
	prevTime time.Time

	progEvery    time.Duration
	lastProg     time.Time
	prevProcCPU  map[int32]float64
	prevProcTime time.Time
	lastProgSet  map[string]bool
}

var errUnknownDevice = errors.New("unknown device")

func main() {
	a := &agent{client: &http.Client{Timeout: 10 * time.Second}}
	var dry bool
	flag.StringVar(&a.rwURL, "remote-write", os.Getenv("STREAM_REMOTE_WRITE"), "Prometheus remote-write URL, e.g. https://prometheus.example.org/api/v1/write")
	flag.StringVar(&a.hub, "hub", os.Getenv("STREAM_HUB"), "hub name shown in Grafana, e.g. Ujjain")
	flag.StringVar(&a.temple, "temple", os.Getenv("STREAM_TEMPLE"), "temple name shown in Grafana (custom backend mode: the kratos temple id)")
	flag.StringVar(&a.name, "name", "", "laptop name shown in Grafana (default: the computer's name)")
	flag.StringVar(&a.user, "username", os.Getenv("STREAM_RW_USERNAME"), "basic-auth user for the Prometheus endpoint")
	flag.StringVar(&a.pass, "password", os.Getenv("STREAM_RW_PASSWORD"), "basic-auth password for the Prometheus endpoint")
	flag.StringVar(&a.token, "token", os.Getenv("AGENT_TOKEN"), "bearer token (instead of username and password)")
	flag.StringVar(&a.server, "server", os.Getenv("STREAM_MONITOR_SERVER"), "custom backend URL (alternative to --remote-write)")
	flag.DurationVar(&a.interval, "interval", 15*time.Second, "how often to measure")
	flag.DurationVar(&a.progEvery, "programs-interval", time.Minute, "how often to report the top programs by CPU and memory (0 turns it off)")
	flag.BoolVar(&dry, "dry-run", false, "just print readings, do not send anything")
	flag.Parse()

	if !dry {
		switch {
		case a.rwURL != "" && (a.hub == "" || a.temple == ""):
			fmt.Fprintln(os.Stderr, "With --remote-write you also need --hub and --temple (names, e.g. --hub Ujjain --temple \"Mahakal temple\").")
			os.Exit(2)
		case a.rwURL == "" && (a.server == "" || a.temple == ""):
			fmt.Fprintln(os.Stderr, "Usage: stream-agent --remote-write https://.../api/v1/write --hub <hub> --temple <temple> [--name <laptop>] [--username u --password p]\n       stream-agent --dry-run   (print readings only)")
			os.Exit(2)
		}
	}
	a.server = strings.TrimRight(a.server, "/")
	if a.rwURL == "" && !dry {
		a.progEvery = 0 // the custom backend has nowhere to put them
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.run(ctx, dry)
}

func (a *agent) run(ctx context.Context, dry bool) {
	if !dry {
		if a.rwURL != "" {
			if _, err := a.gatherSpecs(ctx); err != nil {
				log.Fatalf("cannot read this computer's details: %v", err)
			}
			log.Printf("sending to %s as %s / %s / %s", a.rwURL, a.hub, a.temple, a.name)
		} else {
			a.registerUntilOK(ctx)
		}
		if ctx.Err() != nil {
			return
		}
	}
	// the first reading of a rate needs a starting point, so prime the counters
	_, _ = cpu.Percent(0, false)
	a.prevNet, a.prevTime = readNet(), time.Now()
	if a.progEvery > 0 {
		a.readPrograms(time.Now()) // starting point for per-program CPU
		a.lastProg = time.Now()
	}

	tick := time.NewTicker(a.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("stopping")
			return
		case <-tick.C:
		}
		s := a.measure()
		if dry {
			fmt.Printf("cpu %.0f%%  memory %.0f%%  up %.0f kbps  down %.0f kbps  errors %.1f/min\n", s.CPU, s.Mem, s.NetUp/1000, s.NetDown/1000, s.NetErr)
			for _, u := range s.Programs {
				fmt.Printf("    %-30s cpu %5.1f%%   memory %5.1f%%\n", u.Name, u.CPU, u.Mem)
			}
			continue
		}
		a.queue = append(a.queue, s)
		if cap := queueCap(a.interval); len(a.queue) > cap {
			a.queue = a.queue[len(a.queue)-cap:]
		}
		a.flush(ctx)
	}
}

// ---------- measuring ----------

func (a *agent) measure() Sample {
	now := time.Now()
	s := Sample{TS: now.Unix(), LatencyMs: a.lastLatency}
	if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 { // average since the previous call
		s.CPU = p[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.Mem = vm.UsedPercent
	}
	cur := readNet()
	dt := now.Sub(a.prevTime).Seconds()
	if dt > 0 {
		var sent, recv, errs uint64
		for name, c := range cur {
			p, ok := a.prevNet[name]
			if !ok {
				continue
			}
			sent += delta(c.BytesSent, p.BytesSent)
			recv += delta(c.BytesRecv, p.BytesRecv)
			errs += delta(c.Errin, p.Errin) + delta(c.Errout, p.Errout) + delta(c.Dropin, p.Dropin) + delta(c.Dropout, p.Dropout)
		}
		s.NetUp, s.NetDown, s.NetErr = float64(sent)*8/dt, float64(recv)*8/dt, float64(errs)*60/dt
	}
	a.prevNet, a.prevTime = cur, now
	if a.progEvery > 0 && now.Sub(a.lastProg) >= a.progEvery-time.Second {
		a.lastProg = now
		s.Programs, s.Gone = a.selectTop(a.readPrograms(now))
	}
	return s
}

// a counter that went backwards (interface reset) counts as no traffic rather than a huge number
func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

var virtualName = regexp.MustCompile(`(?i)^(lo\d*|utun\d+|awdl\d+|llw\d+|bridge\d+|gif\d+|stf\d+|anpi\d+|ap\d+|docker\d*|veth.*|br-.*|virbr.*|vmnet.*|vboxnet.*|tun\d+|tap\d+)$|loopback|vethernet|virtualbox|vmware|isatap|teredo|bluetooth|pseudo`)

// only real, connected network interfaces count; loopback, VPN tunnels and VM bridges would double-count traffic
func readNet() map[string]gnet.IOCountersStat {
	out := map[string]gnet.IOCountersStat{}
	ifaces, err := gnet.Interfaces()
	if err != nil {
		return out
	}
	eligible := map[string]bool{}
	for _, i := range ifaces {
		up, loop := false, false
		for _, f := range i.Flags {
			up = up || f == "up"
			loop = loop || f == "loopback"
		}
		if up && !loop && !virtualName.MatchString(i.Name) {
			eligible[i.Name] = true
		}
	}
	counters, err := gnet.IOCounters(true)
	if err != nil {
		return out
	}
	for _, c := range counters {
		if eligible[c.Name] {
			out[c.Name] = c
		}
	}
	return out
}

// ---------- this computer ----------

func (a *agent) gatherSpecs(ctx context.Context) (*host.InfoStat, error) {
	info, _ := host.InfoWithContext(ctx)
	if info == nil {
		return nil, errors.New("could not read this computer's details")
	}
	if a.name == "" {
		a.name = info.Hostname
	}
	a.osName = info.OS
	a.specs = map[string]any{"os_version": strings.TrimSpace(info.Platform + " " + info.PlatformVersion)}
	if ci, err := cpu.InfoWithContext(ctx); err == nil && len(ci) > 0 {
		a.specs["processor"] = strings.TrimSpace(ci[0].ModelName)
	}
	if n, err := cpu.Counts(true); err == nil {
		a.specs["cores"] = n
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		a.specs["ram_gb"] = (vm.Total + (1 << 29)) >> 30
	}
	return info, nil
}

// ---------- sending ----------

func (a *agent) flush(ctx context.Context) {
	if a.rwURL != "" {
		a.flushPrometheus(ctx)
	} else {
		a.flushBackend(ctx)
	}
}

func (a *agent) noteFailure(err error) {
	a.failCount++
	if !a.failing || a.failCount%20 == 0 {
		log.Printf("cannot reach the server (%v); keeping %d readings and retrying", err, len(a.queue))
	}
	a.failing = true
}

func (a *agent) noteRecovered() {
	if a.failing {
		log.Printf("connection restored, sent the backlog")
		a.failing, a.failCount = false, 0
	}
}

// ----- Prometheus -----

func (a *agent) buildSeries(batch []Sample) []rw.Series {
	base := []rw.Label{{Name: "hub", Value: a.hub}, {Name: "temple", Value: a.temple}, {Name: "device", Value: a.name}}
	metrics := []struct {
		name string
		get  func(Sample) float64
	}{
		{"stream_laptop_cpu_percent", func(s Sample) float64 { return s.CPU }},
		{"stream_laptop_memory_percent", func(s Sample) float64 { return s.Mem }},
		{"stream_laptop_network_upload_bps", func(s Sample) float64 { return s.NetUp }},
		{"stream_laptop_network_download_bps", func(s Sample) float64 { return s.NetDown }},
		{"stream_laptop_network_errors_per_minute", func(s Sample) float64 { return s.NetErr }},
		{"stream_laptop_report_latency_ms", func(s Sample) float64 { return s.LatencyMs }},
	}
	out := make([]rw.Series, 0, len(metrics)+1)
	for _, m := range metrics {
		pts := make([]rw.Point, len(batch))
		for i, s := range batch {
			pts[i] = rw.Point{TS: s.TS * 1000, Value: m.get(s)}
		}
		out = append(out, rw.Series{Name: m.name, Labels: base, Samples: pts})
	}
	// hardware details travel as labels on an "info" series whose value is always 1
	info := append([]rw.Label{{Name: "os", Value: a.osName}}, base...)
	for _, k := range []string{"processor", "cores", "ram_gb", "os_version"} {
		if v, ok := a.specs[k]; ok {
			info = append(info, rw.Label{Name: k, Value: fmt.Sprint(v)})
		}
	}
	out = append(out, rw.Series{Name: "stream_laptop_info", Labels: info, Samples: []rw.Point{{TS: batch[len(batch)-1].TS * 1000, Value: 1}}})
	return append(out, a.programSeries(base, batch)...)
}

// One series per program and metric. A program that drops out of the top gets a final "stale" marker,
// so Prometheus stops showing it immediately rather than five minutes later.
func (a *agent) programSeries(base []rw.Label, batch []Sample) []rw.Series {
	type key struct{ metric, program string }
	points := map[key][]rw.Point{}
	var order []key
	add := func(k key, p rw.Point) {
		if _, seen := points[k]; !seen {
			order = append(order, k)
		}
		points[k] = append(points[k], p)
	}
	const cpuM, memM = "stream_laptop_program_cpu_percent", "stream_laptop_program_memory_percent"
	for _, s := range batch {
		ts := s.TS * 1000
		for _, name := range s.Gone {
			add(key{cpuM, name}, rw.Point{TS: ts, Value: rw.StaleNaN})
			add(key{memM, name}, rw.Point{TS: ts, Value: rw.StaleNaN})
		}
		for _, u := range s.Programs {
			add(key{cpuM, u.Name}, rw.Point{TS: ts, Value: u.CPU})
			add(key{memM, u.Name}, rw.Point{TS: ts, Value: u.Mem})
		}
	}
	out := make([]rw.Series, 0, len(order))
	for _, k := range order {
		labels := append(append([]rw.Label(nil), base...), rw.Label{Name: "program", Value: k.program})
		out = append(out, rw.Series{Name: k.metric, Labels: labels, Samples: points[k]})
	}
	return out
}

func (a *agent) flushPrometheus(ctx context.Context) {
	auth := rw.Auth{Username: a.user, Password: a.pass, Token: a.token}
	for len(a.queue) > 0 && ctx.Err() == nil {
		n := min(len(a.queue), maxBatch)
		start := time.Now()
		status, msg, err := rw.Push(ctx, a.client, a.rwURL, auth, a.buildSeries(a.queue[:n]))
		took := time.Since(start)
		switch {
		case err == nil:
			a.lastLatency = float64(took.Milliseconds())
			a.queue = a.queue[n:]
			a.noteRecovered()
		case status == 400:
			// Prometheus refused these readings for good (typically too old after a long outage), so drop them rather than retry forever
			log.Printf("Prometheus rejected %d readings and they were dropped: %s", n, msg)
			a.queue = a.queue[n:]
		default:
			a.noteFailure(err) // unreachable, 5xx, or a wrong password: keep the readings and try again
			return
		}
	}
}

// ----- custom backend (alternative) -----

func (a *agent) call(ctx context.Context, path string, body any) ([]byte, time.Duration, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.server+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	start := time.Now()
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	took := time.Since(start)
	switch {
	case resp.StatusCode == http.StatusNotFound && strings.Contains(path, "/samples"):
		return nil, took, errUnknownDevice
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, took, errors.New("the server rejected the agent token")
	case resp.StatusCode >= 300:
		return nil, took, fmt.Errorf("server said %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, took, nil
}

func (a *agent) register(ctx context.Context) error {
	info, err := a.gatherSpecs(ctx)
	if err != nil {
		return err
	}
	data, _, err := a.call(ctx, "/api/devices/register", map[string]any{
		"machine_id": info.HostID, "name": a.name, "hostname": info.Hostname, "temple_id": a.temple, "os": info.OS, "specs": a.specs,
	})
	if err != nil {
		return err
	}
	var resp struct {
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
	}
	if json.Unmarshal(data, &resp) != nil || resp.Device.ID == "" {
		return fmt.Errorf("unexpected reply: %s", data)
	}
	a.deviceID = resp.Device.ID
	log.Printf("registered as device %s", a.deviceID)
	return nil
}

// Keeps trying, so the agent can start at boot before the network is up.
func (a *agent) registerUntilOK(ctx context.Context) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		err := a.register(ctx)
		if err == nil {
			return
		}
		log.Printf("could not register (%v); retrying in %s", err, wait)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		if wait < time.Minute {
			wait *= 2
		}
	}
}

func (a *agent) flushBackend(ctx context.Context) {
	for len(a.queue) > 0 && ctx.Err() == nil {
		n := min(len(a.queue), 500)
		_, took, err := a.call(ctx, "/api/devices/"+a.deviceID+"/samples", map[string]any{"samples": a.queue[:n]})
		if errors.Is(err, errUnknownDevice) { // the device was removed on the server; register again
			if rerr := a.register(ctx); rerr != nil {
				err = rerr
			} else {
				continue
			}
		}
		if err != nil {
			a.noteFailure(err)
			return
		}
		a.lastLatency = float64(took.Milliseconds())
		a.queue = a.queue[n:]
		a.noteRecovered()
	}
}
