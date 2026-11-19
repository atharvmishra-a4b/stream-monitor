//go:build darwin

package wifi

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	chanRe   = regexp.MustCompile(`^(\S+)\s*\(([^,)]+)`)         // "149 (5GHz, 80MHz)" -> 149, 5GHz
	signalRe = regexp.MustCompile(`(-?\d+)\s*dBm\s*/\s*(-?\d+)`) // "-66 dBm / -95 dBm"
)

// Read parses system_profiler's Wi-Fi section. Used for development on a Mac; the fleet is Windows.
// macOS reports a transmit rate and a signal in dBm, but no separate receive rate.
func Read() *Link {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "system_profiler", "SPAirPortDataType").Output()
	if err != nil {
		return wiredDarwin()
	}

	// Only the block under "Current Network Information:" is the association we are on;
	// everything after "Other Local Wi-Fi Networks:" describes neighbours.
	text := string(out)
	start := strings.Index(text, "Current Network Information:")
	if start < 0 {
		return wiredDarwin()
	}
	text = text[start:]
	if end := strings.Index(text, "Other Local Wi-Fi Networks:"); end > 0 {
		text = text[:end]
	}

	l := &Link{Kind: KindWifi}
	for i, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val := strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch key {
		case "transmit rate":
			l.TxMbps = parseFloat(val)
		case "phy mode":
			l.Radio = val
		case "channel":
			if m := chanRe.FindStringSubmatch(val); m != nil {
				l.Channel, l.Band = m[1], strings.TrimSpace(m[2])
			}
		case "signal / noise":
			if m := signalRe.FindStringSubmatch(val); m != nil {
				l.SignalDBm = parseFloat(m[1])
			}
		default:
			// the SSID is the first indented heading inside the block, printed as "name:" with no value
			if i == 1 && val == "" && key != "" {
				l.SSID = strings.TrimSpace(k)
			}
		}
	}
	if l.TxMbps == 0 && l.SignalDBm == 0 {
		return wiredDarwin()
	}
	return l
}

// wiredDarwin reports an active wired adapter, so a Mac on a cable is described rather than
// reported as nothing. Used for development; the fleet is Windows.
func wiredDarwin() *Link {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ifconfig").Output()
	if err != nil {
		return Unknown("could not list interfaces")
	}
	name := ""
	for _, block := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(block, "en") && strings.Contains(block, "flags=") {
			name = strings.TrimSuffix(strings.Fields(block)[0], ":")
		}
		if name != "" && strings.Contains(block, "status: active") {
			return Wired(name, 0)
		}
	}
	return Unknown("no connected wired or wireless adapter found")
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
