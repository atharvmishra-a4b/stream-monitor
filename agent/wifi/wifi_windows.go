//go:build windows

package wifi

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Read describes the laptop's connection: the Wi-Fi association if there is one, otherwise the
// active wired adapter.
func Read() *Link {
	if l := readWifi(); l != nil {
		return l
	}
	if l := readWired(); l != nil {
		return l
	}
	return Unknown("no connected wired or wireless adapter found")
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // no console flash on a temple laptop
	return cmd
}

// readWifi parses "netsh wlan show interfaces", the one Wi-Fi view available on a stock Windows
// install. Its labels are localised, so on a non-English Windows nothing will match; that is why a
// failure here falls through to the wired adapter rather than being reported as the final answer.
func readWifi() *Link {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	out, err := hidden(exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces")).Output()
	if err != nil {
		return nil // no wireless service, or no adapter at all
	}

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
	if !strings.EqualFold(fields["state"], "connected") {
		return nil // adapter present but not associated: treat as no Wi-Fi
	}

	l := &Link{
		Kind:    KindWifi,
		Adapter: fields["name"],
		SSID:    fields["ssid"],
		Radio:   fields["radio type"],
		Band:    fields["band"],
		Channel: fields["channel"],
	}
	l.RxMbps = parseNum(fields["receive rate (mbps)"])
	l.TxMbps = parseNum(fields["transmit rate (mbps)"])
	l.SignalPercent = parseNum(strings.TrimSuffix(fields["signal"], "%"))

	if l.TxMbps == 0 && l.RxMbps == 0 && l.SignalPercent == 0 {
		return nil // connected, but nothing parsed: most likely a non-English Windows
	}
	return l
}

type netAdapter struct {
	Name                 string `json:"Name"`
	InterfaceDescription string `json:"InterfaceDescription"`
	LinkSpeed            string `json:"LinkSpeed"` // "1 Gbps", "100 Mbps"
	MediaType            string `json:"MediaType"`
	Status               string `json:"Status"`
}

// readWired asks PowerShell for the active adapters. Unlike netsh, the JSON property names are
// English whatever the Windows display language, so this is the locale-safe path.
func readWired() *Link {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	const ps = `Get-NetAdapter -Physical | Where-Object { $_.Status -eq 'Up' } | ` +
		`Select-Object Name,InterfaceDescription,LinkSpeed,MediaType,Status | ConvertTo-Json -Compress`
	out, err := hidden(exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)).Output()
	if err != nil {
		return nil
	}

	body := strings.TrimSpace(string(out))
	if body == "" {
		return nil
	}
	// ConvertTo-Json emits a bare object for a single adapter and an array for several.
	if !strings.HasPrefix(body, "[") {
		body = "[" + body + "]"
	}
	var adapters []netAdapter
	if json.Unmarshal([]byte(body), &adapters) != nil {
		return nil
	}

	// Prefer a wired adapter; skip anything that describes itself as wireless, since a connected
	// Wi-Fi link would already have been returned by readWifi.
	for _, a := range adapters {
		if strings.Contains(strings.ToLower(a.MediaType), "802.11") ||
			strings.Contains(strings.ToLower(a.InterfaceDescription), "wireless") ||
			strings.Contains(strings.ToLower(a.InterfaceDescription), "wi-fi") {
			continue
		}
		name := a.Name
		if name == "" {
			name = a.InterfaceDescription
		}
		return Wired(name, parseLinkSpeed(a.LinkSpeed))
	}
	return nil
}

// parseLinkSpeed turns PowerShell's "1 Gbps" or "100 Mbps" into megabits per second.
func parseLinkSpeed(s string) float64 {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) < 2 {
		return 0
	}
	n := parseNum(f[0])
	switch strings.ToLower(f[1]) {
	case "gbps":
		return n * 1000
	case "mbps":
		return n
	case "kbps":
		return n / 1000
	case "bps":
		return n / 1e6
	}
	return 0
}

// parseNum reads a decimal that may use a comma separator, as some locales print.
func parseNum(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return f
}
