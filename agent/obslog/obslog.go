// Package obslog ships OBS Studio's own log file to a log endpoint.
//
// The payload is GELF, which Graylog accepts natively and any other collector can parse as plain
// JSON. Nothing here assumes Graylog specifically.
//
// Metrics say that something is wrong; this says why. The one line that explained the 19 November
// incident at Khedapati was an ordinary informational line reporting the Wi-Fi rate, carrying no
// error of any kind. Nothing is filtered by default for exactly that reason: the line that cracks a
// case is rarely the one marked as a problem. A full day of logs is about a megabyte per laptop.
//
// Two details make this harder than tailing a file:
//
//   - The agent runs as SYSTEM, OBS runs as the logged-in user, so %APPDATA% inside the agent is
//     not where OBS writes. The user profiles are scanned instead.
//   - OBS stamps each line with a time but no date. The date comes from the file name, and a
//     session running past midnight has to roll it forward.
package obslog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// lineRe matches OBS's prefix: "10:15:05.644: some message".
var lineRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})\.(\d{3}): ?(.*)$`)

// fileRe matches OBS's log file name: "2026-11-19 07-10-10.txt".
var fileRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2}) \d{2}-\d{2}-\d{2}\.txt$`)

const (
	maxBuffered = 20000 // lines held while the endpoint is unreachable, then oldest are dropped
	maxBatch    = 2000  // lines per request
	readLimit   = 8 << 20
)

// Labels identify the laptop, and are attached to every line so a log search lines up with the
// matching Grafana panel.
type Labels struct{ Hub, Temple, Device string }

type Shipper struct {
	url     string
	token   string // sent as x-api-key when set
	appName string // sent as x-afb-app-name when set
	dir     string // empty means: find it
	labels  Labels
	loc     *time.Location
	client  *http.Client
	state   string // file recording how far we have read, so a restart does not resend

	mu       sync.Mutex
	pending  []gelf
	curFile  string
	curOff   int64
	curDate  time.Time
	dropped  int64
	failures int64
	shipped  int64
}

type gelf struct {
	Version   string  `json:"version"`
	Host      string  `json:"host"`
	Short     string  `json:"short_message"`
	Timestamp float64 `json:"timestamp"`
	Level     int     `json:"level"`
	Hub       string  `json:"_hub"`
	Temple    string  `json:"_temple"`
	Device    string  `json:"_device"`
	Source    string  `json:"_source"`
}

func New(url, token, appName, dir, statePath string, labels Labels) *Shipper {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+1800) // Windows may lack the tz database
	}
	return &Shipper{
		url: url, token: token, appName: appName, dir: dir, labels: labels, loc: loc, state: statePath,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Stats reports what has been shipped, dropped and failed, so loss is visible rather than silent.
func (s *Shipper) Stats() (shipped, dropped, failures int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shipped, s.dropped, s.failures
}

// Run reads new log lines and sends them on every tick until ctx ends.
func (s *Shipper) Run(ctx context.Context, every time.Duration) {
	s.loadState()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.saveState()
			return
		case <-tick.C:
		}
		s.collect()
		s.flush(ctx)
	}
}

// ---------- finding the log ----------

// LogDir returns the directory OBS is writing to. When not configured it scans the user profiles,
// because the agent runs as SYSTEM and its own %APPDATA% is not OBS's.
func (s *Shipper) LogDir() string {
	if s.dir != "" {
		return s.dir
	}
	var best string
	var bestMod time.Time
	users, err := os.ReadDir(`C:\Users`)
	if err != nil {
		return ""
	}
	for _, u := range users {
		if !u.IsDir() {
			continue
		}
		d := filepath.Join(`C:\Users`, u.Name(), `AppData\Roaming\obs-studio\logs`)
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		if fi.ModTime().After(bestMod) {
			best, bestMod = d, fi.ModTime()
		}
	}
	return best
}

// newest returns the log file OBS is currently appending to, and the date in its name.
func (s *Shipper) newest() (string, time.Time, bool) {
	dir := s.LogDir()
	if dir == "" {
		return "", time.Time{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}, false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && fileRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", time.Time{}, false
	}
	sort.Strings(names) // the name begins with the date, so this is chronological
	name := names[len(names)-1]

	m := fileRe.FindStringSubmatch(name)
	date, err := time.ParseInLocation("2006-01-02", fmt.Sprintf("%s-%s-%s", m[1], m[2], m[3]), s.loc)
	if err != nil {
		return "", time.Time{}, false
	}
	return filepath.Join(dir, name), date, true
}

// ---------- reading ----------

func (s *Shipper) collect() {
	path, date, ok := s.newest()
	if !ok {
		return
	}

	s.mu.Lock()
	if path != s.curFile {
		// OBS restarted and opened a new file; start at its beginning
		s.curFile, s.curOff, s.curDate = path, 0, date
	}
	off := s.curOff
	s.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() < off {
		off = 0 // truncated: read it again from the start
	}
	if fi.Size() == off {
		return // nothing new
	}
	if _, err := f.Seek(off, 0); err != nil {
		return
	}

	limit := fi.Size() - off
	if limit > readLimit {
		limit = readLimit
	}
	data := make([]byte, limit)
	n, err := io.ReadFull(f, data)
	if n == 0 && err != nil {
		return
	}
	data = data[:n]

	// Stop at the last newline and count the bytes actually consumed, rather than adding up line
	// lengths. Two reasons: OBS writes CRLF, so a line is one byte longer than its text, and OBS is
	// appending as we read, so the tail may be half a line. Shipping that would send a fragment and
	// then send the rest of the same line again.
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return // no complete line yet
	}
	read := int64(end + 1)

	var batch []gelf
	for _, raw := range bytes.Split(data[:end], []byte("\n")) {
		line := strings.TrimRight(string(raw), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		batch = append(batch, s.toGELF(line))
	}

	s.mu.Lock()
	s.curOff = off + read
	s.pending = append(s.pending, batch...)
	if over := len(s.pending) - maxBuffered; over > 0 {
		s.pending = s.pending[over:]
		s.dropped += int64(over)
	}
	s.mu.Unlock()
	s.saveState()
}

// toGELF turns one OBS line into a GELF message, rebuilding the timestamp from the file's date
// and the line's time. A session that runs past midnight sees the time of day go backwards, which
// is taken as the next day.
func (s *Shipper) toGELF(line string) gelf {
	ts := time.Now()
	msg := line
	if m := lineRe.FindStringSubmatch(line); m != nil {
		var h, mi, sec, ms int
		fmt.Sscanf(m[1], "%d", &h)
		fmt.Sscanf(m[2], "%d", &mi)
		fmt.Sscanf(m[3], "%d", &sec)
		fmt.Sscanf(m[4], "%d", &ms)
		msg = m[5]

		s.mu.Lock()
		date := s.curDate
		if date.IsZero() {
			date = time.Now().In(s.loc)
		}
		cand := time.Date(date.Year(), date.Month(), date.Day(), h, mi, sec, ms*int(time.Millisecond), s.loc)
		// rolled past midnight: the file's date no longer applies
		if cand.After(time.Now().Add(time.Hour)) {
			cand = cand.AddDate(0, 0, -1)
		}
		s.mu.Unlock()
		ts = cand
	}

	level := 6 // informational
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, "broken"):
		level = 3
	case strings.Contains(low, "reconnect") || strings.Contains(low, "disconnect") || strings.Contains(low, "warning"):
		level = 4
	}

	return gelf{
		Version: "1.1", Host: s.labels.Device, Short: msg,
		Timestamp: float64(ts.UnixNano()) / 1e9, Level: level,
		Hub: s.labels.Hub, Temple: s.labels.Temple, Device: s.labels.Device, Source: "obs",
	}
}

// ---------- sending ----------

func (s *Shipper) flush(ctx context.Context) {
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.mu.Unlock()
			return
		}
		n := min(len(s.pending), maxBatch)
		batch := append([]gelf(nil), s.pending[:n]...)
		s.mu.Unlock()

		if err := s.send(ctx, batch); err != nil {
			s.mu.Lock()
			s.failures++
			s.mu.Unlock()
			return // keep the backlog and try again next tick
		}

		s.mu.Lock()
		s.pending = s.pending[n:]
		s.shipped += int64(n)
		s.mu.Unlock()
	}
}

// send posts the batch as a JSON array of GELF objects.
//
// Graylog's own HTTP input wants newline-delimited objects, but anything behind API Gateway and
// Lambda reads the body with a single json.loads, which fails on the second line. An array is the
// one shape both can read, so it is what we send.
func (s *Shipper) send(ctx context.Context, batch []gelf) error {
	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(batch); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "stream-agent")
	// Set() would canonicalise these to "X-Api-Key" and "X-Afb-App-Name". Header names are
	// case-insensitive, so a correct server accepts either, but assigning the map entries directly
	// puts exactly the lowercase names on the wire. (HTTP/2 lowercases all names anyway.)
	if s.token != "" {
		req.Header["x-api-key"] = []string{s.token}
	}
	if s.appName != "" {
		req.Header["x-afb-app-name"] = []string{s.appName}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("log endpoint said %d", resp.StatusCode)
	}
	return nil
}

// ---------- remembering how far we read ----------

type state struct {
	File string `json:"file"`
	Off  int64  `json:"offset"`
}

func (s *Shipper) loadState() {
	if s.state == "" {
		return
	}
	b, err := os.ReadFile(s.state)
	if err != nil {
		return
	}
	var st state
	if json.Unmarshal(b, &st) != nil || st.File == "" {
		return // empty or damaged: start over rather than trust a bad offset
	}
	s.mu.Lock()
	s.curFile, s.curOff = st.File, st.Off
	s.mu.Unlock()
}

func (s *Shipper) saveState() {
	if s.state == "" {
		return
	}
	s.mu.Lock()
	st := state{File: s.curFile, Off: s.curOff}
	s.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	// Write then rename: os.WriteFile truncates first, so a laptop losing power mid-write would
	// leave an empty file and the whole log would be shipped again on the next start.
	_ = os.MkdirAll(filepath.Dir(s.state), 0o755)
	tmp := s.state + ".tmp"
	if os.WriteFile(tmp, b, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, s.state)
}
