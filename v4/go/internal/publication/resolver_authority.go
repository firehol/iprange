//go:build !windows

// Selection and reconstruction of one exact publication authority
// (Rust publication/resolver_authority.rs): the caller-supplied
// result facts and the discovered reservation are reconciled into
// one header, one exact reservation, and optionally one later
// coordination owner.

package publication

// baseResolution is the reconciled authority of one resolution (Rust
// BaseResolution).
type baseResolution struct {
	destination *destination
	header      reservationHeader
	seed        seed
	exact       *inspectedReservation
	later       *inspectedReservation
}

// authority is the intermediate reconciliation result (Rust
// Authority).
type authority struct {
	header reservationHeader
	exact  *inspectedReservation
	later  *inspectedReservation
}

// inspectResolution binds the destination, derives the supplied
// result header (when one is given), and reconciles it with the
// discovered reservation (Rust resolver_authority::inspect).
func inspectResolution(path string, supplied *PublicationResult, check func() error) (baseResolution, error) {
	destination, err := bindDestination(path)
	if err != nil {
		return baseResolution{}, err
	}
	var suppliedHeader *reservationHeader
	if supplied != nil {
		header, err := resultHeaderFor(supplied, destination)
		if err != nil {
			destination.directory().Close()
			return baseResolution{}, err
		}
		suppliedHeader = &header
	}
	auth, err := inspectAuthority(destination, suppliedHeader, check)
	if err != nil {
		destination.directory().Close()
		return baseResolution{}, err
	}
	seed, err := reconstructSeed(destination, auth.header)
	if err != nil {
		destination.directory().Close()
		return baseResolution{}, err
	}
	return baseResolution{
		destination: destination,
		header:      auth.header,
		seed:        seed,
		exact:       auth.exact,
		later:       auth.later,
	}, nil
}

// inspectAuthority discovers the bound reservation and fills the
// exact owner from the private scan when the supplied header demands
// it (Rust inspect_authority).
func inspectAuthority(destination *destination, suppliedHeader *reservationHeader, check func() error) (authority, error) {
	discovered, err := discoverReservation(destination, check)
	if err != nil {
		return authority{}, err
	}
	auth, err := chooseAuthority(suppliedHeader, discovered)
	if err != nil {
		if discovered != nil {
			_ = discovered.Close()
		}
		return authority{}, err
	}
	if suppliedHeader != nil && auth.exact == nil {
		exact, err := exactPrivateReservation(destination, *suppliedHeader, check)
		if err != nil {
			return authority{}, err
		}
		auth.exact = exact
	}
	return auth, nil
}

// chooseAuthority reconciles the supplied header with the discovered
// reservation (Rust choose_authority; the five arms are exact: no
// authority, supplied only, discovered only, matching pair, same
// attempt conflict, canonical later, foreign private conflict).
func chooseAuthority(supplied *reservationHeader, discovered *inspectedReservation) (authority, error) {
	switch {
	case supplied == nil && discovered == nil:
		return authority{}, unresolvable("no publication result or bound reservation is available")
	case supplied != nil && discovered == nil:
		return authority{header: *supplied}, nil
	case supplied == nil && discovered != nil:
		return authority{header: discovered.header, exact: discovered}, nil
	case *supplied == discovered.header:
		return authority{header: *supplied, exact: discovered}, nil
	case supplied.attemptID == discovered.header.attemptID:
		return authority{}, conflictProblem("caller result and reservation disagree for the same attempt")
	case discovered.location == reservationLocationCanonical:
		return authority{header: *supplied, later: discovered}, nil
	default:
		return authority{}, conflictProblem("another private publication attempt is bound to the destination")
	}
}
