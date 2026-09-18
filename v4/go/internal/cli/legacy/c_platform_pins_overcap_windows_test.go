//go:build windows

package legacy

import (
	"strings"
	"testing"
)

// TestOverCapPinIsThePlatformsOwnMessage is the Windows half of
// TestPlatformPinRenderingsAreEffective. It lives here because the code it
// names, ERROR_INVALID_NAME, exists only on this target.
//
// The assertions are the reason the substitution is allowed at all: the
// message half must be the text this platform formats for the code its open
// failed with, it must not be the glibc text the pins carry, and the
// substitution must actually have taken place. So the rendering can never
// degrade into "some error text" and pass.
func TestOverCapPinIsThePlatformsOwnMessage(t *testing.T) {
	const lineCap = "iprange: n - " + posixLineCapMessage + "\n"
	got := overCapPin(lineCap)
	if got == lineCap {
		t.Error("overCapPin kept the glibc text on Windows, where the message half is the platform's own")
	}
	if !strings.Contains(got, errorInvalidName.Error()) {
		t.Errorf("overCapPin must print the message the platform formats for ERROR_INVALID_NAME (%d): %q",
			int(errorInvalidName), got)
	}
	if strings.Contains(got, posixLineCapMessage) {
		t.Errorf("overCapPin left the glibc text in the diagnostic: %q", got)
	}
	if strings.TrimSpace(errorInvalidName.Error()) == "" {
		t.Error("the platform formatted no message for ERROR_INVALID_NAME, so the pin would pin nothing")
	}
}
