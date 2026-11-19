//go:build windows

package wifi

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Read parses "netsh wlan show interfaces", the one Wi-Fi view available on a stock Windows
// install with no extra software.
//
// Caveat: netsh labels are localised. The parser below expects an English Windows. On a machine
// running another display language the labels will not match and Read returns nil, so the Wi-Fi
// metrics are simply absent rather than wrong. Check with `netsh wlan show interfaces`.
func Read() *Link {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // no console flash on a temple laptop
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Lines look like "    Receive rate (Mbps)    : 1441". Collect them as key/value.
	fields := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			fields[k] = v
		}
	}

	// "disconnected" still prints a few fields; only a live association is a reading.
	if !strings.EqualFold(fields["state"], "connected") {
		return nil
	}

	l := &Link{
		SSID:    fields["ssid"],
		Radio:   fields["radio type"],
		Band:    fields["band"],
		Channel: fields["channel"],
	}
	l.RxMbps = parseNum(fields["receive rate (mbps)"])
	l.TxMbps = parseNum(fields["transmit rate (mbps)"])
	l.SignalPercent = parseNum(strings.TrimSuffix(fields["signal"], "%"))

	if l.TxMbps == 0 && l.RxMbps == 0 && l.SignalPercent == 0 {
		return nil // nothing usable parsed: most likely a non-English Windows
	}
	return l
}

// parseNum reads a decimal that may use a comma separator, as some locales print.
func parseNum(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.Fields(s)[0], 64)
	if err != nil {
		return 0
	}
	return f
}
