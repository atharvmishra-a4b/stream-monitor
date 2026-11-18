package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Streams only run at set times, so a laptop that is switched off between streams is normal, not a problem.
// The schedule says when laptops are expected to be online. One schedule applies to every temple for now.

type window struct{ start, end int } // minutes after midnight

type Schedule struct {
	loc     *time.Location
	windows []window
	early   int // laptops should be on this many minutes before a window starts
	always  bool
}

type ScheduleInfo struct {
	Active bool   `json:"active"` // laptops are expected to be online right now
	Text   string `json:"text"`
}

const defaultWindows = "07:00-12:00,16:00-20:00"

// parseSchedule reads windows like "07:00-12:00,16:00-20:00" in the given timezone.
// "always" turns schedule awareness off, so a missing laptop is always a problem.
func parseSchedule(spec, tz string, earlyMinutes int) (Schedule, error) {
	s := Schedule{early: earlyMinutes}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return s, fmt.Errorf("unknown TIMEZONE %q: %w", tz, err)
	}
	s.loc = loc
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, "always") {
		s.always = true
		return s, nil
	}
	if spec == "" {
		spec = defaultWindows
	}
	for _, part := range strings.Split(spec, ",") {
		a, b, ok := strings.Cut(strings.TrimSpace(part), "-")
		start, err1 := parseClock(a)
		end, err2 := parseClock(b)
		if !ok || err1 != nil || err2 != nil || end <= start {
			return s, fmt.Errorf("bad stream window %q: use HH:MM-HH:MM with the end after the start", part)
		}
		s.windows = append(s.windows, window{start, end})
	}
	sort.Slice(s.windows, func(i, j int) bool { return s.windows[i].start < s.windows[j].start })
	return s, nil
}

func parseClock(v string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func clock(minutes int) string {
	return time.Date(2000, 1, 1, minutes/60, minutes%60, 0, 0, time.UTC).Format("3:04 PM")
}

func (s Schedule) minutes(now time.Time) int {
	t := now.In(s.loc)
	return t.Hour()*60 + t.Minute()
}

// Expected reports whether laptops should be online now: during a window, or shortly before one starts.
func (s Schedule) Expected(now time.Time) bool {
	if s.always {
		return true
	}
	m := s.minutes(now)
	for _, w := range s.windows {
		if m >= w.start-s.early && m < w.end {
			return true
		}
	}
	return false
}

func (s Schedule) Describe(now time.Time) ScheduleInfo {
	if s.always {
		return ScheduleInfo{true, "Laptops are expected to be online at all times"}
	}
	m := s.minutes(now)
	for _, w := range s.windows {
		if m >= w.start && m < w.end {
			return ScheduleInfo{true, "Streaming until " + clock(w.end)}
		}
	}
	for _, w := range s.windows {
		if m >= w.start-s.early && m < w.start {
			return ScheduleInfo{true, "Stream starts at " + clock(w.start)}
		}
	}
	for _, w := range s.windows {
		if w.start > m {
			return ScheduleInfo{false, "Next stream today at " + clock(w.start)}
		}
	}
	return ScheduleInfo{false, "Next stream tomorrow at " + clock(s.windows[0].start)}
}
