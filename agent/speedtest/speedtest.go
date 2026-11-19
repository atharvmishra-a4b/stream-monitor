// Package speedtest measures how fast this laptop can actually download, right now.
//
// The network figures elsewhere in the agent count bytes that happened to move, and the Wi-Fi link
// rate describes only the hop to the access point. Neither answers "is the internet at this temple
// actually working well at this moment", which is the question asked when a stream looks wrong and
// the people on site report that everything is fine.
//
// It downloads a fixed amount over several connections at once. One connection cannot fill a modern
// line: TCP window size and round-trip time hold a single stream well below the real capacity, so
// four run in parallel and their throughput is added up.
//
// The figure is deliberately measured after a short warm-up. A fresh TCP connection starts slow and
// speeds up, and on a fast line a small download is over before it reaches full speed, which reads
// far below the truth. Skipping the first moments measures the steady state instead.
//
// On a healthy line a small download finishes too quickly to measure well, so the reading
// understates it. That error shrinks as the line gets worse: when a download crawls, it is timed
// accurately. Readings are therefore a reliable baseline to compare against, not an exact capacity.
package speedtest

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultURL is Cloudflare's speed-test endpoint. It serves an arbitrary number of bytes and is
// close to most networks. It rate-limits bursts, so a failed attempt is recorded rather than
// reported as a speed of zero.
const DefaultURL = "https://speed.cloudflare.com/__down?bytes=%d"

const (
	warmUp     = 500 * time.Millisecond // ignored, to let the connections reach full speed
	maxRun     = 60 * time.Second       // a very slow line must not hold the test open forever
	minMeasure = 100 * time.Millisecond // too short a window to divide by
)

// Result is one completed measurement.
type Result struct {
	Mbps     float64       // steady-state download speed
	Duration time.Duration // how long the whole attempt took
	Bytes    int64         // bytes actually received
	At       time.Time
}

type Tester struct {
	urlFormat string
	bytes     int64 // total across all connections
	parallel  int
	client    *http.Client

	mu       sync.RWMutex
	latest   *Result
	failures int64
}

func New(urlFormat string, totalBytes int64, parallel int) *Tester {
	if urlFormat == "" {
		urlFormat = DefaultURL
	}
	if parallel < 1 {
		parallel = 4
	}
	return &Tester{
		urlFormat: urlFormat,
		bytes:     totalBytes,
		parallel:  parallel,
		// no client timeout: the run is bounded by context instead, so a slow line is measured
		// rather than cut off as an error
		client: &http.Client{},
	}
}

// Latest is the most recent successful measurement, or nil if none has completed.
func (t *Tester) Latest() *Result {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.latest
}

// Failures counts attempts that could not complete: no connection, a refusal from the far end,
// or a line too slow to finish within the time limit. A rising count is itself a symptom.
func (t *Tester) Failures() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.failures
}

// Run measures once now, then again on every tick until ctx ends.
func (t *Tester) Run(ctx context.Context, every time.Duration) {
	t.once(ctx)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.once(ctx)
		}
	}
}

func (t *Tester) once(ctx context.Context) {
	r, err := t.measure(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil || r == nil {
		t.failures++
		return
	}
	t.latest = r
}

func (t *Tester) measure(ctx context.Context) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, maxRun)
	defer cancel()

	perConn := t.bytes / int64(t.parallel)
	if perConn < 1 {
		perConn = t.bytes
	}

	var received atomic.Int64
	var failed atomic.Int64
	var wg sync.WaitGroup
	started := time.Now()

	for i := 0; i < t.parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := t.stream(ctx, perConn, &received); err != nil {
				failed.Add(1)
			}
		}()
	}

	// Let the connections get up to speed, then start timing. Everything received before this
	// point is deliberately excluded from the rate.
	select {
	case <-ctx.Done():
	case <-time.After(warmUp):
	}
	atStart, from := received.Load(), time.Now()

	wg.Wait()
	window := time.Since(from)
	total := received.Load()

	// Every connection failing means no measurement, not a speed of zero.
	if failed.Load() == int64(t.parallel) {
		return nil, fmt.Errorf("all %d connections failed", t.parallel)
	}
	if window < minMeasure {
		return nil, fmt.Errorf("finished in %v, too short to measure", window)
	}
	measured := total - atStart
	if measured <= 0 {
		return nil, fmt.Errorf("no data in the measured window")
	}

	return &Result{
		Mbps:     float64(measured) * 8 / window.Seconds() / 1e6,
		Duration: time.Since(started),
		Bytes:    total,
		At:       time.Now(),
	}, nil
}

func (t *Tester) stream(ctx context.Context, want int64, received *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(t.urlFormat, want), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "stream-agent")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("server said %d", resp.StatusCode) // 403 when the endpoint rate-limits
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			received.Add(int64(n))
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil // io.EOF: this connection finished its share
		}
	}
}
