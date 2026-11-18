package main

import (
	"testing"
	"time"
)

func TestAssess(t *testing.T) {
	now := time.Now()
	seen := func(ago time.Duration) *time.Time { v := now.Add(-ago); return &v }
	cases := []struct {
		name     string
		d        Device
		expected bool
		want     string
	}{
		{"never reported, stream due", Device{}, true, StateOffline},
		{"never reported, no stream due", Device{}, false, StateStandby},
		{"gone quiet, stream due", Device{LastSeenAt: seen(5 * time.Minute)}, true, StateOffline},
		{"gone quiet, no stream due", Device{LastSeenAt: seen(5 * time.Minute)}, false, StateStandby},
		{"fine", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{CPU: 30, Mem: 50}}, true, StateOK},
		{"fine outside hours (left on)", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{CPU: 30, Mem: 50}}, false, StateOK},
		{"cpu pegged", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{CPU: 97}}, true, StateProblem},
		{"problems still count outside hours", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{CPU: 97}}, false, StateProblem},
		{"memory full", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{Mem: 95}}, true, StateProblem},
		{"dropping packets", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{NetErr: 40}}, true, StateProblem},
		{"slow link", Device{LastSeenAt: seen(5 * time.Second), Latest: Sample{LatencyMs: 3000}}, true, StateProblem},
		{"offline hides old problems", Device{LastSeenAt: seen(10 * time.Minute), Latest: Sample{CPU: 99}}, true, StateOffline},
	}
	for _, c := range cases {
		if got := assess(c.d, now, 90, c.expected).State; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func ist(t *testing.T, hhmm string) time.Time {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-17 "+hhmm, loc)
	return v.UTC() // the server compares in UTC; the schedule must convert it back to IST
}

func TestSchedule(t *testing.T) {
	s, err := parseSchedule("", "Asia/Kolkata", 30)
	if err != nil {
		t.Fatal(err)
	}
	expect := map[string]bool{
		"03:00": false, "06:15": false, "06:30": true, "06:59": true, "07:00": true, "11:59": true,
		"12:00": false, "14:00": false, "15:29": false, "15:30": true, "19:59": true, "20:00": false, "23:30": false,
	}
	for at, want := range expect {
		if got := s.Expected(ist(t, at)); got != want {
			t.Errorf("expected online at %s IST: got %v, want %v", at, got, want)
		}
	}
	text := map[string]string{
		"09:00": "Streaming until 12:00 PM", "06:40": "Stream starts at 7:00 AM",
		"13:00": "Next stream today at 4:00 PM", "21:00": "Next stream tomorrow at 7:00 AM",
	}
	for at, want := range text {
		if got := s.Describe(ist(t, at)).Text; got != want {
			t.Errorf("describe at %s: got %q, want %q", at, got, want)
		}
	}
}

func TestScheduleParsing(t *testing.T) {
	if s, err := parseSchedule("always", "Asia/Kolkata", 30); err != nil || !s.Expected(ist(t, "03:00")) {
		t.Errorf("always should expect laptops at any hour (err %v)", err)
	}
	for _, bad := range []string{"7-12", "12:00-07:00", "07:00-07:00", "banana"} {
		if _, err := parseSchedule(bad, "Asia/Kolkata", 30); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if _, err := parseSchedule("", "Mars/Olympus", 30); err == nil {
		t.Error("an unknown timezone should be rejected")
	}
}
