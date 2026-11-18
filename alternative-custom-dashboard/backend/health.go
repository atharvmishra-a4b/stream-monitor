package main

import (
	"fmt"
	"time"
)

// Health rules live in one place so the hub, temple and device pages always agree.

const (
	StateOffline = "offline" // nothing received recently, and a laptop should be online now
	StateStandby = "standby" // not reporting, but no stream is due, so that is normal
	StateProblem = "problem" // reporting, but something needs attention
	StateOK      = "ok"
)

type Health struct {
	State       string   `json:"state"`
	LastSeenAge *float64 `json:"last_seen_age"` // seconds; null if the device has never reported
	Problems    []string `json:"problems"`
}

// expected says whether laptops should be online right now (see schedule.go).
func assess(d Device, now time.Time, offlineAfter float64, expected bool) Health {
	quiet := StateOffline
	if !expected {
		quiet = StateStandby
	}
	h := Health{Problems: []string{}}
	if d.LastSeenAt == nil {
		h.State = quiet
		return h
	}
	age := now.Sub(*d.LastSeenAt).Seconds()
	h.LastSeenAge = &age
	if age > offlineAfter {
		h.State = quiet
		return h
	}
	l := d.Latest
	if l.CPU > 90 {
		h.Problems = append(h.Problems, fmt.Sprintf("CPU is at %.0f%%", l.CPU))
	}
	if l.Mem > 93 {
		h.Problems = append(h.Problems, fmt.Sprintf("Memory is at %.0f%%", l.Mem))
	}
	if l.NetErr > 10 {
		h.Problems = append(h.Problems, fmt.Sprintf("Network is dropping packets (%.0f errors per minute)", l.NetErr))
	}
	if l.LatencyMs > 1500 {
		h.Problems = append(h.Problems, fmt.Sprintf("Slow connection to the monitor (%.1f s per report)", l.LatencyMs/1000))
	}
	if len(h.Problems) > 0 {
		h.State = StateProblem
	} else {
		h.State = StateOK
	}
	return h
}
