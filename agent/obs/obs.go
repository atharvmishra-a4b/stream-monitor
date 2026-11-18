// Package obs reads live streaming figures from OBS Studio over its built-in WebSocket server
// (obs-websocket v5, shipped with OBS 28 and later; enable it in Tools > WebSocket Server Settings).
//
// It answers the question the laptop's own CPU and network cannot: is the stream actually healthy?
// Dropped frames, reconnects and the real output bitrate all come from here.
//
// A Client keeps one connection open and refreshes in the background. Readings are a snapshot:
// Latest returns nil whenever OBS is closed or unreachable, and the caller marks those series stale.
package obs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Stats is one reading of OBS's state. Counters are cumulative since OBS started and reset when it
// restarts, so they are reported as Prometheus counters and read with rate().
type Stats struct {
	Streaming    bool
	Reconnecting bool
	Congestion   float64 // 0..1, OBS's own view of how close the stream is to failing

	OutputSkippedFrames float64 // frames dropped on the way out: network could not keep up
	OutputTotalFrames   float64
	RenderSkippedFrames float64 // frames dropped while rendering: the laptop could not keep up
	RenderTotalFrames   float64
	OutputBytes         float64 // bytes sent by the stream itself, not the whole machine

	FPS                float64
	FrameRenderTimeMs  float64
	AvailableDiskGB    float64
	StreamDurationSecs float64
}

type Client struct {
	url, password string
	every         time.Duration

	mu     sync.RWMutex
	latest *Stats
}

// New returns a client for an OBS at url (for example ws://127.0.0.1:4455).
// Password may be empty if OBS is configured without authentication.
func New(url, password string, every time.Duration) *Client {
	if every <= 0 {
		every = 5 * time.Second
	}
	return &Client{url: url, password: password, every: every}
}

// Latest is the most recent reading, or nil if OBS is not currently reachable.
func (c *Client) Latest() *Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

func (c *Client) set(s *Stats) {
	c.mu.Lock()
	c.latest = s
	c.mu.Unlock()
}

// Run keeps a connection to OBS open until ctx ends, reconnecting when OBS is closed or restarted.
// OBS not running is the normal case on a laptop that is not streaming, so it is never an error.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := c.session(ctx); err != nil && ctx.Err() == nil {
			c.set(nil) // OBS went away: stop reporting stale figures
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

// session connects, authenticates, then polls until the connection drops.
func (c *Client) session(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, c.url, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	if err := c.identify(ctx, conn); err != nil {
		return err
	}

	tick := time.NewTicker(c.every)
	defer tick.Stop()
	for {
		s, err := c.poll(ctx, conn)
		if err != nil {
			return err
		}
		c.set(s)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// ---- protocol ----

type message struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type helloData struct {
	RPCVersion     int `json:"rpcVersion"`
	Authentication *struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication"`
}

// identify completes the obs-websocket handshake: Hello (op 0) in, Identify (op 1) out,
// Identified (op 2) back. When OBS requires a password the Hello carries a challenge and salt.
func (c *Client) identify(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var hello helloData
	if err := readOp(ctx, conn, 0, &hello); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	ident := map[string]any{"rpcVersion": hello.RPCVersion, "eventSubscriptions": 0}
	if hello.Authentication != nil {
		if c.password == "" {
			return fmt.Errorf("OBS requires a password (set --obs-password)")
		}
		ident["authentication"] = authResponse(c.password, hello.Authentication.Salt, hello.Authentication.Challenge)
	}
	if err := writeOp(ctx, conn, 1, ident); err != nil {
		return err
	}
	// A wrong password closes the socket here rather than returning an error message.
	if err := readOp(ctx, conn, 2, &struct{}{}); err != nil {
		return fmt.Errorf("identify (wrong password?): %w", err)
	}
	return nil
}

// authResponse is obs-websocket's scheme: base64(sha256(base64(sha256(password+salt)) + challenge)).
func authResponse(password, salt, challenge string) string {
	sum := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(sum[:])
	sum = sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(sum[:])
}

type requestResponse struct {
	RequestID     string `json:"requestId"`
	RequestStatus struct {
		Result  bool   `json:"result"`
		Comment string `json:"comment"`
	} `json:"requestStatus"`
	ResponseData json.RawMessage `json:"responseData"`
}

// request sends one Request (op 6) and waits for its RequestResponse (op 7).
// Only one request is in flight at a time, so matching on requestId is enough.
func request(ctx context.Context, conn *websocket.Conn, id, requestType string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writeOp(ctx, conn, 6, map[string]any{"requestType": requestType, "requestId": id}); err != nil {
		return err
	}
	var resp requestResponse
	if err := readOp(ctx, conn, 7, &resp); err != nil {
		return err
	}
	if !resp.RequestStatus.Result {
		return fmt.Errorf("%s: %s", requestType, resp.RequestStatus.Comment)
	}
	if len(resp.ResponseData) == 0 {
		return nil
	}
	return json.Unmarshal(resp.ResponseData, out)
}

type statsData struct {
	CPUUsage               float64 `json:"cpuUsage"`
	AvailableDiskSpace     float64 `json:"availableDiskSpace"` // megabytes
	ActiveFps              float64 `json:"activeFps"`
	AverageFrameRenderTime float64 `json:"averageFrameRenderTime"` // milliseconds
	RenderSkippedFrames    float64 `json:"renderSkippedFrames"`
	RenderTotalFrames      float64 `json:"renderTotalFrames"`
	OutputSkippedFrames    float64 `json:"outputSkippedFrames"`
	OutputTotalFrames      float64 `json:"outputTotalFrames"`
}

type streamStatusData struct {
	OutputActive        bool    `json:"outputActive"`
	OutputReconnecting  bool    `json:"outputReconnecting"`
	OutputDuration      float64 `json:"outputDuration"` // milliseconds
	OutputCongestion    float64 `json:"outputCongestion"`
	OutputBytes         float64 `json:"outputBytes"`
	OutputSkippedFrames float64 `json:"outputSkippedFrames"`
	OutputTotalFrames   float64 `json:"outputTotalFrames"`
}

func (c *Client) poll(ctx context.Context, conn *websocket.Conn) (*Stats, error) {
	var gs statsData
	if err := request(ctx, conn, "stats", "GetStats", &gs); err != nil {
		return nil, err
	}
	var ss streamStatusData
	if err := request(ctx, conn, "stream", "GetStreamStatus", &ss); err != nil {
		return nil, err
	}
	return &Stats{
		Streaming:    ss.OutputActive,
		Reconnecting: ss.OutputReconnecting,
		Congestion:   ss.OutputCongestion,
		// while streaming, GetStreamStatus counts this stream only; GetStats counts since OBS started
		OutputSkippedFrames: pick(ss.OutputActive, ss.OutputSkippedFrames, gs.OutputSkippedFrames),
		OutputTotalFrames:   pick(ss.OutputActive, ss.OutputTotalFrames, gs.OutputTotalFrames),
		RenderSkippedFrames: gs.RenderSkippedFrames,
		RenderTotalFrames:   gs.RenderTotalFrames,
		OutputBytes:         ss.OutputBytes,
		FPS:                 gs.ActiveFps,
		FrameRenderTimeMs:   gs.AverageFrameRenderTime,
		AvailableDiskGB:     gs.AvailableDiskSpace / 1024,
		StreamDurationSecs:  ss.OutputDuration / 1000,
	}, nil
}

func pick(cond bool, a, b float64) float64 {
	if cond {
		return a
	}
	return b
}

// ---- framing ----

func writeOp(ctx context.Context, conn *websocket.Conn, op int, d any) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	body, err := json.Marshal(message{Op: op, D: raw})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, body)
}

// readOp reads until a message with the wanted op arrives, skipping anything else
// (OBS can interleave events even with eventSubscriptions set to 0).
func readOp(ctx context.Context, conn *websocket.Conn, want int, out any) error {
	for {
		_, body, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var m message
		if err := json.Unmarshal(body, &m); err != nil {
			return err
		}
		if m.Op != want {
			continue
		}
		if len(m.D) == 0 {
			return nil
		}
		return json.Unmarshal(m.D, out)
	}
}
