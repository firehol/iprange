//go:build windows

// Windows stub of the live install machinery: the whole live surface
// refuses before any path access (lock_refuse.go), so the namespace
// mutations are typed refusals, exactly like namespace_windows.go.

package live

import "os"

func install(private, canonical string, privateFile *os.File, privateIdentity FileIdentity, previous *FileIdentity, policy LiveResetPolicy) error {
	return liveRefusal()
}

func installNoreplace(private, canonical string, privateFile *os.File, expected FileIdentity) error {
	return liveRefusal()
}

func installReplaceDiscarding(private, canonical string, privateFile *os.File, expectedPrivate, expectedCanonical FileIdentity) error {
	return liveRefusal()
}

func installExchange(private, canonical string, privateFile *os.File, expectedPrivate, expectedCanonical FileIdentity) error {
	return liveRefusal()
}
