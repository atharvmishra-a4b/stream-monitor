//go:build !windows && !darwin

package wifi

// Read is not implemented for this platform; the fleet is Windows and development is on macOS.
// Returning nil simply leaves the Wi-Fi metrics absent.
func Read() *Link { return nil }
