// Every answered platform is subtracted by its own GOOS name (the
// answer-half comment explains why aliases are not relied on); android
// is then re-added explicitly because bionic refuses despite Go's
// linux umbrella.
//go:build (!linux && !darwin && !ios && !tvos && !watchos && !freebsd && !netbsd && !dragonfly) || android

package legacy

import "net"

// legacyNumericAnswer has no local numeric answer on the refusing
// platforms — the exact complement of dns_numeric_answer.go's tag set,
// so every GOOS lands on exactly one half. The basis per answering
// platform (and per refusal) is documented in dns_numeric_answer.go.
//
// Windows refuses: Winsock reports WSAHOST_NOT_FOUND for the
// inet_aton(3) shapes, verified on the authorized Windows validation
// host across this wave's legs (the cross-engine-numeric consensus step
// re-proves it against the Rust authority at every leg).
//
// OpenBSD refuses: its getaddrinfo accepts only strict
// dotted-quad/IPv6 numeric forms (getaddrinfo.3: "a numeric host address
// string consisting of a dotted decimal IPv4 address or an IPv6
// address"). OpenBSD is not in the qualified run set today, so the
// claim rests on the documented interface rather than an executed leg;
// any future OpenBSD leg must run both halves of the pin.
//
// Android is ACTIVE here despite Go's linux umbrella tag: bionic
// refuses the forms, verified at platform/bionic tag
// android-14.0.0_r35 libc/dns/net/getaddrinfo.c — the only inet_aton
// call (line 959) sits inside #if 0 /*X/Open spec*/ (line 957), the live
// AF_INET path is inet_pton (line 980), and the file header states
// "disallow classful form for IPv4 (due to use of inet_pton)". The
// answer half therefore excludes android explicitly ((…) && !android)
// and this half takes it with (|| android); Rust's target_os="android"
// is a distinct value that never enters its answer cfg set.
// The Apple family (darwin/ios/tvos/watchos) is a member of the ANSWER
// half — one shared Libinfo resolver, cited in dns_numeric_answer.go;
// the terms are spelled explicitly because only GOOS=ios aliases the
// darwin build tag.
//
// Platforms outside the qualified set (solaris, illumos, aix, plan9,
// ...) fall into this half by DEFAULT, conservatively: not qualified
// targets, resolvers not evidenced here; any future qualification must
// revisit this tag set against platform resolver evidence first
// (dns_numeric_answer.go lists the basis per answering platform). On the
// platforms this half names with executed or documented evidence —
// windows (native legs) and openbsd (its documented interface) — the
// Rust authority delegates to the same refusing resolver and fails the
// forms too, which is what TestDNSNumericFormsAreNotAnsweredHere pins
// per platform. Answering them on this half
// would make the Go engine exit 0 with content where the authority
// exits 1 with a DNS failure — a divergence on the contractual
// dimensions (rc and output), not message text. The refusal is pinned
// by TestDNSNumericFormsAreNotAnsweredHere, which every native leg runs.
func legacyNumericAnswer(string) ([]net.IP, bool) { return nil, false }

// numericFormsAnswerHere states whether this build's authority answers
// the glibc numeric forms.
const numericFormsAnswerHere = false
