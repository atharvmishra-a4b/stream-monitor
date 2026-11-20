// Package config lets a fleet of laptops be retuned from one file instead of one machine at a time.
//
// The agent fetches a small JSON document over HTTP at startup and re-reads it periodically, so a
// change to, say, how often the speed test runs reaches every temple without anyone visiting a
// laptop. Settings that can be changed safely while running are applied immediately; the rest take
// effect at the next restart, which in practice is the same day, since these laptops are switched
// off overnight.
//
// Three rules keep a central file from becoming a single point of failure:
//
//   - A flag given on the command line always wins. One laptop can be pinned for debugging without
//     editing the file everyone shares.
//   - A fetch that fails is never fatal. The last good document is cached on disk, so a laptop that
//     boots while its internet is down still starts with the settings it had, and keeps measuring.
//   - The laptop's identity (hub, temple, name) is never taken from here. A mistake in a shared file
//     should not be able to rename the fleet.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Duration accepts Go's duration syntax ("5s", "2m") from JSON.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration like \"5s\": %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Settings is every value that may be set centrally. Pointers distinguish "not mentioned" from
// "set to zero", which matters because zero turns several of these off.
type Settings struct {
	RemoteWrite       *string   `json:"remote_write"`
	Interval          *Duration `json:"interval"`
	WifiInterval      *Duration `json:"wifi_interval"`
	ProgramsInterval  *Duration `json:"programs_interval"`
	SpeedtestInterval *Duration `json:"speedtest_interval"`
	SpeedtestBytes    *int      `json:"speedtest_bytes"`
	SpeedtestParallel *int      `json:"speedtest_parallel"`
	SpeedtestURL      *string   `json:"speedtest_url"`
	LogURL            *string   `json:"log_url"`
	LogToken          *string   `json:"log_token"`
	LogAppName        *string   `json:"log_app_name"`
	LogInterval       *Duration `json:"log_interval"`
	ObsLogDir         *string   `json:"obs_log_dir"`
}

// File is the document served over HTTP. Two shapes are accepted.
//
// The simple one is a flat object that applies to every laptop:
//
//	{ "interval": "5s", "speedtest_bytes": 10 }
//
// The other adds per-temple overrides for sites that genuinely differ, such as a laptop on gigabit
// Ethernet wanting a larger speed-test sample than one on weak Wi-Fi:
//
//	{ "defaults": { ... }, "temples": { "Some temple": { ... } } }
//
// Starting with the flat form and growing into the second needs no migration: a file that mentions
// neither "defaults" nor "temples" is read as settings for everyone.
type File struct {
	Defaults Settings            `json:"defaults"`
	Temples  map[string]Settings `json:"temples"`
}

// merge lays over on top of base, field by field, ignoring anything over does not mention.
func merge(base, over Settings) Settings {
	out := base
	if over.RemoteWrite != nil {
		out.RemoteWrite = over.RemoteWrite
	}
	if over.Interval != nil {
		out.Interval = over.Interval
	}
	if over.WifiInterval != nil {
		out.WifiInterval = over.WifiInterval
	}
	if over.ProgramsInterval != nil {
		out.ProgramsInterval = over.ProgramsInterval
	}
	if over.SpeedtestInterval != nil {
		out.SpeedtestInterval = over.SpeedtestInterval
	}
	if over.SpeedtestBytes != nil {
		out.SpeedtestBytes = over.SpeedtestBytes
	}
	if over.SpeedtestParallel != nil {
		out.SpeedtestParallel = over.SpeedtestParallel
	}
	if over.SpeedtestURL != nil {
		out.SpeedtestURL = over.SpeedtestURL
	}
	if over.LogURL != nil {
		out.LogURL = over.LogURL
	}
	if over.LogToken != nil {
		out.LogToken = over.LogToken
	}
	if over.LogAppName != nil {
		out.LogAppName = over.LogAppName
	}
	if over.LogInterval != nil {
		out.LogInterval = over.LogInterval
	}
	if over.ObsLogDir != nil {
		out.ObsLogDir = over.ObsLogDir
	}
	return out
}

// Resolve picks the settings for one temple: shared defaults with that temple's overrides on top.
func (f *File) Resolve(temple string) Settings {
	return merge(f.Defaults, f.Temples[temple])
}

type Loader struct {
	url, cachePath, temple string
	client                 *http.Client

	mu   sync.RWMutex
	last Settings
	seen bool
}

func New(url, cachePath, temple string) *Loader {
	return &Loader{
		url:       url,
		cachePath: cachePath,
		temple:    temple,
		// short: a laptop booting with no internet must not wait on this
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Current is the settings last successfully resolved, and whether any have been.
func (l *Loader) Current() (Settings, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.last, l.seen
}

// Load fetches the document, resolves this temple's settings and caches the raw file. If the fetch
// fails it falls back to the cached copy, so a laptop that boots offline still gets its settings.
func (l *Loader) Load(ctx context.Context) (Settings, error) {
	body, err := l.fetch(ctx)
	if err != nil {
		cached, cerr := os.ReadFile(l.cachePath)
		if cerr != nil {
			return Settings{}, fmt.Errorf("fetch failed (%w) and no cached config", err)
		}
		s, perr := l.parse(cached)
		if perr != nil {
			return Settings{}, fmt.Errorf("fetch failed (%w) and cached config is unusable: %v", err, perr)
		}
		l.store(s)
		return s, fmt.Errorf("using cached config: %w", err)
	}

	s, err := l.parse(body)
	if err != nil {
		return Settings{}, err // a malformed document must not overwrite a good cache
	}
	if l.cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(l.cachePath), 0o755)
		_ = os.WriteFile(l.cachePath, body, 0o644)
	}
	l.store(s)
	return s, nil
}

func (l *Loader) store(s Settings) {
	l.mu.Lock()
	l.last, l.seen = s, true
	l.mu.Unlock()
}

func (l *Loader) parse(body []byte) (Settings, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		return Settings{}, fmt.Errorf("config is not valid JSON: %w", err)
	}
	_, keyed := keys["defaults"]
	if _, ok := keys["temples"]; ok {
		keyed = true
	}

	if !keyed {
		// flat: the whole object is settings for every laptop
		var s Settings
		if err := json.Unmarshal(body, &s); err != nil {
			return Settings{}, fmt.Errorf("config is not valid JSON: %w", err)
		}
		return s, nil
	}

	var f File
	if err := json.Unmarshal(body, &f); err != nil {
		return Settings{}, fmt.Errorf("config is not valid JSON: %w", err)
	}
	return f.Resolve(l.temple), nil
}

func (l *Loader) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stream-agent")
	req.Header.Set("Cache-Control", "no-cache") // a CDN must not serve a stale config for hours
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("server said %d", resp.StatusCode)
	}
	return readAll(resp)
}

func readAll(resp *http.Response) ([]byte, error) {
	const maxConfig = 1 << 20 // a config this large is a mistake, not a config
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < maxConfig {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
	return buf, fmt.Errorf("config larger than %d bytes", maxConfig)
}

// Watch re-reads the document on every tick and calls onChange whenever the resolved settings
// differ from the previous ones. Errors are reported but never stop the loop: a central file that
// becomes unreachable must leave the fleet running on what it already had.
func (l *Loader) Watch(ctx context.Context, every time.Duration, onChange func(Settings), onError func(error)) {
	if every <= 0 {
		return
	}
	prev, _ := l.Current()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s, err := l.Load(ctx)
		if err != nil {
			if onError != nil {
				onError(err)
			}
			continue
		}
		if !equal(prev, s) {
			prev = s
			onChange(s)
		}
	}
}

func equal(a, b Settings) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
