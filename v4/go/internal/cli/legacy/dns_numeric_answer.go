// Explicit sets, no reliance on tag aliasing: GOOS=ios aliases the
// darwin term but tvos/watchos do not, so the Apple family is spelled
// out; android is SUBTRACTED because Go's linux term is an umbrella
// that also matches GOOS=android, while bionic REFUSES the forms (the
// refused-half file cites the source) and Rust's target_os="android" is
// a distinct value outside its answering cfg set.
//go:build (linux || darwin || ios || tvos || watchos || freebsd || netbsd || dragonfly) && !android

package legacy

import "net"

// legacyNumericAnswer is the half of the glibc numeric short-circuit for
// the platforms whose system resolver answers the inet_aton(3) forms:
// the C oracle (Linux) and the Rust authority (which delegates every
// unix lookup to libc getaddrinfo) accept "0x7f000001", "0x7f.1", "0xA"
// etc. locally on these targets, so Go's pure resolver must answer them
// too or it diverges from the authority on valid input. The IPv6 family
// passes AF_UNSPEC hints, where the same IPv4 numeric forms are accepted
// and returned mapped, so this applies to both families (collectAddrs
// applies the C family policy to the returned value).
//
// Per-platform basis (round-2 review; upstream sources read 2026-09-17):
//
//   - linux/glibc: answers; proven against the C oracle on this host.
//     linux/musl also answers (src/network/lookup_ipliteral.c calls
//     __inet_aton, which parses with strtoul base 0, i.e. 0x hex), so
//     the linux tag is correct for both libcs; the gnu/musl distinction
//     does not matter for this rule.
//
//   - freebsd: answers; lib/libc/net/getaddrinfo.c explore_numeric()
//     calls inet_aton() for AF_INET (KAME lineage; the FreeBSD 15.1
//     inet_aton(3) manpage documents hex/octal parts).
//
//   - netbsd: answers; lib/libc/net/getaddrinfo.c explore_numeric()
//     calls inet_aton() for AF_INET ($KAME 1.29 lineage, read from the
//     NetBSD source tree).
//
//   - darwin: answers. Apple's resolver is not Libc's net/ (which has
//     no getaddrinfo.c) but apple-oss-distributions/Libinfo
//     lookup.subproj/si_getaddrinfo.c, whose _gai_numerichost falls back
//     to _inet_aton_check(name, a4, 1) after inet_pton (lines 775-779),
//     and _inet_aton_check (Libc net/FreeBSD/inet_addr.c:115) parses
//     "0x=hex, 0=octal" (line 129). Verified against those sources
//     2026-09-17; a native darwin leg must still execute both halves of
//     the pin before the claim is called proven on the platform.
//
//   - dragonfly: answers; lib/libc/net/getaddrinfo.c explore_numeric()
//     calls inet_aton() for AF_INET (FreeBSD 2008 import + KAME, read
//     from the upstream tree 2026-09-17). A native leg must still
//     execute the pin before the claim is called proven there.
//
//   - ios / tvos / watchos: answer with darwin. Libinfo and its
//     _gai_numerichost -> _inet_aton_check numeric path are one shared
//     system library across the Apple platforms (single Xcode SDK), so
//     the darwin citation governs the family. Spelled explicitly because
//     only GOOS=ios aliases the darwin build term; a native leg for any
//     of them must still execute both halves of the pin.
//
//   - android (GOOS): REFUSES and is excluded from this half explicitly.
//     Go's linux tag is an umbrella over android; bionic's live
//     AF_INET path is inet_pton (the inet_aton call sits inside
//     #if 0 /*X/Open spec*/, cited in full in dns_numeric_refuse.go),
//     and Rust's target_os="android" is a distinct value outside its
//     answering cfg set — so all three agree on refusal.
//
// The discriminating evidence for this half runs under the canonical
// CGO_ENABLED=0 build (the battery and every staged leg pin it): with
// cgo linked, the fallback resolver routes through libc getaddrinfo,
// which answers the hex forms itself on the platforms above, so removing
// the emulation would not be noticed. The refusal half
// (dns_numeric_refuse.go) discriminates under either linkage, because no
// refusing platform's resolver answers the forms.
func legacyNumericAnswer(host string) ([]net.IP, bool) {
	v4, err := inetAton(host)
	if err != nil {
		return nil, false
	}
	return []net.IP{net.IPv4(byte(v4>>24), byte(v4>>16), byte(v4>>8), byte(v4))}, true
}

// numericFormsAnswerHere states whether this build's authority answers
// the glibc numeric forms. Test pins measured against those answers
// scope themselves out of the refusing platforms with it, and the
// refusal behavior is pinned there instead.
const numericFormsAnswerHere = true
