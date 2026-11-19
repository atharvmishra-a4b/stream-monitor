// Package wifi reads the laptop's current Wi-Fi link quality: the negotiated rates, and how strong
// the signal is.
//
// This is the pipe, not the water. The network figures elsewhere in the agent measure bytes actually
// moved, which stays low while nothing is streaming and cannot tell a healthy idle link from a
// crippled one. The link rate says how much room there is, whether or not anything is using it, and
// it costs nothing to read: no test traffic, no competing with a live stream.
//
// Read returns nil when there is no Wi-Fi (wired laptops, or a machine where the OS tool is absent
// or speaks another language). That is reported as "no reading", never as an error worth stopping for.
package wifi

import "time"

// Link is one reading of the current Wi-Fi association.
type Link struct {
	TxMbps float64 // negotiated transmit (upload) rate
	RxMbps float64 // negotiated receive rate; 0 where the OS does not report it separately

	// Signal strength, reported differently per platform. SignalPercent is 0 when unknown,
	// SignalDBm is 0 when unknown. Typically only one is populated.
	SignalPercent float64 // Windows: 0..100
	SignalDBm     float64 // macOS, Linux: usually -30 (excellent) to -90 (unusable)

	SSID    string
	Radio   string // 802.11ax, 802.11ac, ...
	Band    string // "2.4 GHz", "5 GHz", "6 GHz" where known
	Channel string
}

// readTimeout bounds the OS command; a wedged helper must never stall the measurement loop.
const readTimeout = 5 * time.Second
