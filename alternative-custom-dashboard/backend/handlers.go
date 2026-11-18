package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Server struct {
	cfg     Config
	store   *Store
	catalog Catalog
	sched   Schedule
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	// used by the dashboard
	mux.HandleFunc("GET /api/hubs", s.handleHubs)
	mux.HandleFunc("GET /api/hubs/{id}", s.handleHub)
	mux.HandleFunc("GET /api/temples", s.handleTemples)
	mux.HandleFunc("GET /api/devices/{id}", s.handleDevice)
	mux.HandleFunc("GET /api/devices/{id}/series", s.handleSeries)
	mux.HandleFunc("PATCH /api/devices/{id}", s.handleUpdateDevice)
	// used by the agents on the laptops
	mux.HandleFunc("POST /api/devices/register", s.handleRegister)
	mux.HandleFunc("POST /api/devices/{id}/samples", s.handleSamples)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	fail(w, http.StatusBadGateway, err.Error())
}

func (s *Server) agentAllowed(r *http.Request) bool {
	if s.cfg.AgentToken == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AgentToken)) == 1
}

// ---- fleet view shared by the hub list and the hub page ----

type DeviceSummary struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	TempleID string   `json:"temple_id"`
	CPU      *float64 `json:"cpu"`
	Mem      *float64 `json:"mem"`
	NetUp    *float64 `json:"net_up"`
	Health
}

type Counts struct {
	Total   int `json:"total"`
	OK      int `json:"ok"`
	Problem int `json:"problem"`
	Offline int `json:"offline"`
	Standby int `json:"standby"`
}

func (c *Counts) add(state string) {
	c.Total++
	switch state {
	case StateOK:
		c.OK++
	case StateProblem:
		c.Problem++
	case StateStandby:
		c.Standby++
	default:
		c.Offline++
	}
}

// worst-first, so the device that needs attention is always the first thing on screen
var stateOrder = map[string]int{StateProblem: 0, StateOffline: 1, StateOK: 2, StateStandby: 3}

func (s *Server) fleet(r *http.Request) ([]DeviceSummary, error) {
	devices, err := s.store.ListActive(r.Context())
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expected := s.sched.Expected(now)
	out := make([]DeviceSummary, 0, len(devices))
	for _, d := range devices {
		h := assess(d, now, s.cfg.OfflineAfter, expected)
		sum := DeviceSummary{ID: d.ID, Name: d.Name, Hostname: d.Hostname, OS: d.OS, TempleID: d.TempleID, Health: h}
		if h.State == StateOK || h.State == StateProblem {
			cpu, mem, up := d.Latest.CPU, d.Latest.Mem, d.Latest.NetUp
			sum.CPU, sum.Mem, sum.NetUp = &cpu, &mem, &up
		}
		out = append(out, sum)
	}
	sort.SliceStable(out, func(i, j int) bool { return stateOrder[out[i].State] < stateOrder[out[j].State] })
	return out, nil
}

type stripCell struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

func (s *Server) handleHubs(w http.ResponseWriter, r *http.Request) {
	cat, err := s.catalog.Load(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	fleet, err := s.fleet(r)
	if err != nil {
		s.internal(w, err)
		return
	}
	type hubRow struct {
		Hub
		Temples int         `json:"temples"`
		Devices Counts      `json:"devices"`
		Strip   []stripCell `json:"strip"`
	}
	templeHub := map[string]string{}
	rows := map[string]*hubRow{}
	for _, h := range cat.Hubs {
		rows[h.ID] = &hubRow{Hub: h, Strip: []stripCell{}}
	}
	for _, t := range cat.Temples {
		templeHub[t.ID] = t.HubID
		if row := rows[t.HubID]; row != nil {
			row.Temples++
		}
	}
	orphans := 0
	for _, d := range fleet {
		row := rows[templeHub[d.TempleID]]
		if row == nil {
			orphans++
			continue
		}
		row.Devices.add(d.State)
		row.Strip = append(row.Strip, stripCell{d.ID, d.Name, d.State})
	}
	out := make([]*hubRow, 0, len(rows))
	total := Counts{}
	for _, h := range cat.Hubs {
		row := rows[h.ID]
		out = append(out, row)
		total.Total += row.Devices.Total
		total.OK += row.Devices.OK
		total.Problem += row.Devices.Problem
		total.Offline += row.Devices.Offline
		total.Standby += row.Devices.Standby
	}
	// hubs with devices first, most urgent on top
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Devices, out[j].Devices
		if (a.Total > 0) != (b.Total > 0) {
			return a.Total > 0
		}
		return a.Problem+a.Offline > b.Problem+b.Offline
	})
	writeJSON(w, http.StatusOK, map[string]any{"hubs": out, "totals": total, "devices_in_unknown_temples": orphans, "schedule": s.sched.Describe(time.Now())})
}

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	cat, err := s.catalog.Load(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	hub, ok := cat.hub(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "This hub does not exist in kratos.")
		return
	}
	fleet, err := s.fleet(r)
	if err != nil {
		s.internal(w, err)
		return
	}
	type templeRow struct {
		Temple
		Devices []DeviceSummary `json:"devices"`
	}
	byID := map[string]*templeRow{}
	for _, t := range cat.Temples {
		if t.HubID == hub.ID {
			byID[t.ID] = &templeRow{Temple: t, Devices: []DeviceSummary{}}
		}
	}
	counts := Counts{}
	for _, d := range fleet {
		if row := byID[d.TempleID]; row != nil {
			row.Devices = append(row.Devices, d)
			counts.add(d.State)
		}
	}
	rows := []*templeRow{}
	empty := []Temple{}
	for _, t := range cat.Temples {
		row := byID[t.ID]
		if row == nil {
			continue
		}
		if len(row.Devices) > 0 {
			rows = append(rows, row)
		} else {
			empty = append(empty, t)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return stateOrder[rows[i].Devices[0].State] < stateOrder[rows[j].Devices[0].State]
	})
	writeJSON(w, http.StatusOK, map[string]any{"hub": hub, "temples": rows, "temples_without_devices": empty, "devices": counts})
}

func (s *Server) handleTemples(w http.ResponseWriter, r *http.Request) {
	cat, err := s.catalog.Load(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	type row struct {
		Temple
		HubName string `json:"hub_name"`
	}
	out := make([]row, 0, len(cat.Temples))
	for _, t := range cat.Temples {
		h, _ := cat.hub(t.HubID)
		out = append(out, row{t, h.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"temples": out})
}

// ---- one device ----

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		fail(w, http.StatusNotFound, "This device is not registered.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	cat, err := s.catalog.Load(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	temple, ok := cat.temple(d.TempleID)
	if !ok {
		temple = Temple{ID: d.TempleID, Name: "Unknown temple " + short(d.TempleID), HubID: unassignedHubID}
	}
	hub, ok := cat.hub(temple.HubID)
	if !ok {
		hub = Hub{ID: unassignedHubID, Name: "No hub assigned"}
	}
	var latest *Sample
	if d.LastSeenAt != nil {
		latest = &d.Latest
	}
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"device": d, "temple": temple, "hub": hub, "latest": latest,
		"health": assess(d, now, s.cfg.OfflineAfter, s.sched.Expected(now)), "schedule": s.sched.Describe(now),
	})
}

var ranges = map[string]struct{ span, step time.Duration }{
	"15m": {15 * time.Minute, 15 * time.Second},
	"1h":  {time.Hour, 30 * time.Second},
	"6h":  {6 * time.Hour, 3 * time.Minute},
	"24h": {24 * time.Hour, 12 * time.Minute},
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Get(r.Context(), id); errors.Is(err, errNotFound) {
		fail(w, http.StatusNotFound, "This device is not registered.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	name := r.URL.Query().Get("range")
	rg, ok := ranges[name]
	if !ok {
		name, rg = "1h", ranges["1h"]
	}
	end := time.Now()
	start := end.Add(-rg.span)
	pts, err := s.store.Series(r.Context(), id, start, int(rg.step.Seconds()))
	if err != nil {
		s.internal(w, err)
		return
	}
	series := map[string][][2]float64{"cpu": {}, "memory": {}, "net_up_bps": {}, "net_down_bps": {}, "net_errors": {}, "latency_ms": {}}
	add := func(key string, t int64, v *float64) {
		if v != nil && !math.IsNaN(*v) {
			series[key] = append(series[key], [2]float64{float64(t), math.Round(*v*100) / 100})
		}
	}
	for _, p := range pts {
		add("cpu", p.T, p.CPU)
		add("memory", p.T, p.Mem)
		add("net_up_bps", p.T, p.Up)
		add("net_down_bps", p.T, p.Down)
		add("net_errors", p.T, p.Err)
		add("latency_ms", p.T, p.Latency)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range": name, "start": start.Unix(), "end": end.Unix(), "step": int(rg.step.Seconds()), "series": series,
	})
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string `json:"name"`
		TempleID *string `json:"temple_id"`
		Active   *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Send a JSON body with name, temple_id or active.")
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) == "" {
		fail(w, http.StatusBadRequest, "A device needs a name.")
		return
	}
	if body.TempleID != nil {
		cat, err := s.catalog.Load(r.Context())
		if err != nil {
			s.internal(w, err)
			return
		}
		if _, ok := cat.temple(*body.TempleID); !ok {
			fail(w, http.StatusBadRequest, "That temple does not exist in kratos.")
			return
		}
	}
	d, err := s.store.Update(r.Context(), r.PathValue("id"), body.Name, body.TempleID, body.Active)
	if errors.Is(err, errNotFound) {
		fail(w, http.StatusNotFound, "This device is not registered.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": d})
}

// ---- agent endpoints ----

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.agentAllowed(r) {
		fail(w, http.StatusUnauthorized, "Agent token is missing or wrong.")
		return
	}
	var d Device
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&d); err != nil {
		fail(w, http.StatusBadRequest, "Send a JSON body.")
		return
	}
	d.MachineID, d.TempleID = strings.TrimSpace(d.MachineID), strings.TrimSpace(d.TempleID)
	if d.MachineID == "" || d.TempleID == "" {
		fail(w, http.StatusBadRequest, "machine_id and temple_id are required.")
		return
	}
	cat, err := s.catalog.Load(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	if _, ok := cat.temple(d.TempleID); !ok {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Temple %s does not exist in kratos.", d.TempleID))
		return
	}
	if d.Name == "" {
		d.Name = d.Hostname
	}
	if d.Name == "" {
		d.Name = "Device " + short(d.MachineID)
	}
	if len(d.Specs) == 0 {
		d.Specs = json.RawMessage(`{}`)
	}
	saved, err := s.store.Register(r.Context(), d)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": saved})
}

const maxBatch = 500

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Min(math.Max(v, lo), hi)
}

// Agents send readings in batches: usually one, but after a network outage they send everything
// they kept, so the history has no hole in it.
func (s *Server) handleSamples(w http.ResponseWriter, r *http.Request) {
	if !s.agentAllowed(r) {
		fail(w, http.StatusUnauthorized, "Agent token is missing or wrong.")
		return
	}
	d, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		fail(w, http.StatusNotFound, "Unknown device. Register again.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	var body struct {
		Samples []Sample `json:"samples"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Send {\"samples\": [...]}.")
		return
	}
	if len(body.Samples) == 0 || len(body.Samples) > maxBatch {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Send between 1 and %d samples.", maxBatch))
		return
	}
	now := time.Now().Unix()
	keep := body.Samples[:0]
	for _, x := range body.Samples {
		if x.TS < now-int64(s.cfg.RetentionDays)*86400 || x.TS > now+300 {
			continue // too old to keep, or from the future (clock wrong)
		}
		x.CPU, x.Mem = clamp(x.CPU, 0, 100), clamp(x.Mem, 0, 100)
		x.NetUp, x.NetDown = clamp(x.NetUp, 0, 1e12), clamp(x.NetDown, 0, 1e12)
		x.NetErr, x.LatencyMs = clamp(x.NetErr, 0, 1e9), clamp(x.LatencyMs, 0, 1e7)
		keep = append(keep, x)
	}
	if len(keep) > 0 {
		if err := s.store.AddSamples(r.Context(), d.ID, keep); err != nil {
			s.internal(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"stored": len(keep)})
}
