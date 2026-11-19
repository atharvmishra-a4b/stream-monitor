// Package wifi reads how this laptop is connected to the network, and how good that connection is.
//
// Despite the name it covers wired links too. A temple on Ethernet and a temple on Wi-Fi should
// appear side by side in the same place, and an absent reading should never be ambiguous: a laptop
// on a cable and a laptop whose Wi-Fi could not be read look identical if the answer is simply
// "no data", which is the one thing this must avoid.
//
// This is the pipe, not the water. The network figures elsewhere in the agent measure bytes actually
// moved, which stays low while nothing is streaming and cannot tell a healthy idle link from a
// crippled one. The link rate says how much room there is, whether or not anything is using it, and
// it costs nothing to read: no test traffic, no competing with a live stream.
package wifi

import "time"

// Kind is how the laptop is attached to the network.
type Kind string

const (
	KindWifi     Kind = "wifi"
	KindEthernet Kind = "ethernet"
	KindUnknown  Kind = "unknown" // nothing could be read; Reason says what went wrong
)

// Link is one reading of the current connection.
type Link struct {
	Kind   Kind
	Reason string // only for KindUnknown: why there is no reading

	Adapter string // "Wi-Fi", "Ethernet", or the adapter's own name

	TxMbps float64 // Wi-Fi: negotiated transmit rate. Ethernet: link speed.
	RxMbps float64 // 0 where the platform does not report it separately

	// Signal strength, Wi-Fi only and reported differently per platform. Both are 0 when unknown,
	// and a wired link has neither.
	SignalPercent float64 // Windows: 0..100
	SignalDBm     float64 // macOS, Linux: about -30 (excellent) to -90 (unusable)

	// Wi-Fi only.
	SSID    string
	Radio   string // 802.11ax, 802.11ac, ...
	Band    string // "2.4 GHz", "5 GHz", "6 GHz" where known
	Channel string
}

// Wired returns a reading for a laptop on a cable, with the link speed if it is known.
func Wired(adapter string, mbps float64) *Link {
	if adapter == "" {
		adapter = "Ethernet"
	}
	return &Link{Kind: KindEthernet, Adapter: adapter, TxMbps: mbps, RxMbps: mbps}
}

// Unknown returns a reading that records why nothing could be determined, so that a wired laptop
// is never confused with a failed read.
func Unknown(reason string) *Link {
	return &Link{Kind: KindUnknown, Reason: reason}
}

// readTimeout bounds the OS command; a wedged helper must never stall the measurement loop.
const readTimeout = 8 * time.Second
