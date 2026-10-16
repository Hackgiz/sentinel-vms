package homekit

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/brutella/hap"
)

// Keys in homekit_state that are ours, not the HAP library's. The library's
// own keys ("keypair", "uuid", "<name>.pairing", "version", ...) are never
// prefixed, so these cannot collide.
const (
	keySetupPin = "sentinel.setup_pin" // 8 digits
	keySetupID  = "sentinel.setup_id"  // 4 characters, 0-9 A-Z
)

// categoryBridge is the HAP accessory category advertised in the QR payload.
const categoryBridge = 2

// newSetupPin returns a random 8-digit setup code the HAP spec allows.
func newSetupPin() (string, error) {
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
		if err != nil {
			return "", err
		}
		pin := fmt.Sprintf("%08d", n.Int64())
		if !hap.InvalidPins[pin] {
			return pin, nil
		}
	}
}

// newSetupID returns the 4-character setup id that goes into the QR payload.
func newSetupID() (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 4)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}

// formatPin renders "12345678" as "123-45-678".
func formatPin(pin string) string {
	if len(pin) != 8 {
		return pin
	}
	return pin[:3] + "-" + pin[3:5] + "-" + pin[5:]
}

// setupURI builds the X-HM:// payload Apple's Home app reads from the QR
// code: category, the IP transport flag and the setup code packed into 45
// bits, base 36, nine characters, followed by the setup id.
func setupURI(pin, setupID string) string {
	var code uint64
	if _, err := fmt.Sscanf(pin, "%d", &code); err != nil {
		return ""
	}
	const flagIP = 2
	payload := uint64(categoryBridge)<<31 | uint64(flagIP)<<27 | code
	const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	out := make([]byte, 9)
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = digits[payload%36]
		payload /= 36
	}
	return "X-HM://" + string(out) + strings.ToUpper(setupID)
}
