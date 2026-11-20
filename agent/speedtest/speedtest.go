// Package speedtest measures how fast this laptop can actually download, right now.
//
// The network figures elsewhere in the agent count bytes that happened to move, and the Wi-Fi link
// rate describes only the hop to the access point. Neither answers "is the internet at this temple
// actually working well at this moment", which is the question asked when a stream looks wrong and
// the people on site report that everything is fine.
//
// Pointed at a file it downloads the whole thing; pointed at an endpoint that serves an arbitrary
// number of bytes it asks for a set amount. One connection cannot fill a line to a distant
// endpoint, because TCP window size and round-trip time hold a single stream well below the real
// capacity, so several can run in parallel and their throughput is added up. Against a nearby CDN
// that is unnecessary: one connection saturates the line.
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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultURL is Cloudflare's speed-test endpoint, which serves however many bytes are asked for.
//
// Two shapes of URL are accepted. One containing "%d" is handed the byte count, as Cloudflare's is.
// Any other URL is treated as a fixed file and fetched with a Range request, so pointing this at an
// object on your own CDN needs no cooperation from the server beyond range support, which S3 and
// CloudFront both have. Measuring against your own CDN tests the path that matters, and removes a
// dependency on somebody else's free endpoint.
const DefaultURL = "https://speed.cloudflare.com/__down?bytes=%d"

const (
	// Timing starts once this fraction of the download has arrived, rather than after a fixed
	// delay. A fresh TCP connection starts slow, so the opening of a run reads low; skipping a
	// share of the bytes rather than a number of milliseconds means the same rule works on a
	// 5 Mbit/s line and a gigabit one. A fixed delay does not: on a fast link the whole download
	// lands inside it, leaving nothing to measure.
	warmUpFraction = 0.25
	pollEvery      = 10 * time.Millisecond
	maxRun         = 60 * time.Second      // a very slow line must not hold the test open forever
	minMeasure     = 10 * time.Millisecond // too short a window to divide by meaningfully
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

// New builds a tester. totalBytes of 0 means "whatever the file is": the URL is fetched entire,
// with one connection, which is the natural way to use an object on your own CDN. A byte count is
// still needed for an endpoint that generates data on demand, such as Cloudflare's.
func New(urlFormat string, totalBytes int64, parallel int) *Tester {
	if urlFormat == "" {
		urlFormat = DefaultURL
	}
	if parallel < 1 {
		parallel = 4
	}
	if strings.Contains(urlFormat, "%d") && totalBytes <= 0 {
		totalBytes = 10_000_000 // an endpoint serving on demand has to be told how much
	}
	if totalBytes <= 0 {
		// whole-file mode: splitting it across connections would need a byte count, and asking
		// several connections for the same file would simply download it several times over
		parallel = 1
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
	if t.bytes > 0 && perConn < 1 {
		perConn = t.bytes
	}

	var received atomic.Int64
	var failed atomic.Int64
	var wg sync.WaitGroup
	started := time.Now()

	for i := 0; i < t.parallel; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := t.stream(ctx, idx, perConn, &received); err != nil {
				failed.Add(1)
			}
		}(i)
	}

	// Start timing once the warm-up share has arrived. On a fast line that happens within a few
	// milliseconds; on a slow one it takes a while. Either way the rest of the run is measured.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	// With no byte count the size is unknown until the download finishes, so there is nothing to
	// take a fraction of; the whole run is timed instead.
	want := int64(float64(t.bytes) * warmUpFraction)
	if t.bytes <= 0 {
		want = 1 << 62 // never reached: fall through to timing the whole run
	}
	var atStart int64
	from := time.Now()
waiting:
	for {
		select {
		case <-done:
			break waiting // whole run finished before the warm-up share: fall back below
		case <-ctx.Done():
			break waiting
		case <-time.After(pollEvery):
			if received.Load() >= want {
				atStart, from = received.Load(), time.Now()
				break waiting
			}
		}
	}

	<-done
	window := time.Since(from)
	total := received.Load()

	// Every connection failing means no measurement, not a speed of zero.
	if failed.Load() == int64(t.parallel) {
		return nil, fmt.Errorf("all %d connections failed", t.parallel)
	}
	if total <= 0 {
		return nil, fmt.Errorf("no data received")
	}

	measured, over := total-atStart, window
	// A download that finished before the warm-up share was reached is a very fast line, not a
	// failure. Time the whole run instead: slightly pessimistic, but a reading rather than a gap.
	if measured <= 0 || window < minMeasure {
		measured, over = total, time.Since(started)
	}
	if over < minMeasure {
		return nil, fmt.Errorf("finished in %v, too short to measure", over)
	}

	return &Result{
		Mbps:     float64(measured) * 8 / over.Seconds() / 1e6,
		Duration: time.Since(started),
		Bytes:    total,
		At:       time.Now(),
	}, nil
}

func (t *Tester) stream(ctx context.Context, idx int, want int64, received *atomic.Int64) error {
	url, byteRange := t.urlFormat, ""
	switch {
	case strings.Contains(t.urlFormat, "%d"):
		url = fmt.Sprintf(t.urlFormat, want) // endpoint that serves a requested number of bytes
	case want > 0:
		// A fixed file, with a size asked for: take this connection's slice. With one connection
		// that is the first `want` bytes; with several, each takes a different stretch so the file
		// is downloaded once between them rather than once each.
		lo := int64(idx) * want
		byteRange = fmt.Sprintf("bytes=%d-%d", lo, lo+want-1)
	}
	// no byte count and a fixed file: fetch all of it, no Range header

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "stream-agent")
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 206 is the expected reply to a Range request; 200 means the server ignored it and is
	// sending the whole file, which is still usable data.
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("server said %d", resp.StatusCode) // 403 when an endpoint rate-limits
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
