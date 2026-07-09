package iprangedb

import "testing"

// The oracle: random set/delete sequences vs. an independent in-memory interval map,
// comparing scan + count after every op, in BOTH families. A deterministic LCG (the same
// constants as the Rust test) makes failures reproducible.

type orec struct {
	from, to uint32
	scope    uint8
}

func oracleDelete(o []orec, from, to uint32) []orec {
	out := make([]orec, 0, len(o)+2)
	for _, r := range o {
		if r.to < from || r.from > to {
			out = append(out, r) // no overlap
			continue
		}
		if r.from < from {
			out = append(out, orec{r.from, from - 1, r.scope})
		}
		if r.to > to {
			out = append(out, orec{to + 1, r.to, r.scope})
		}
	}
	return out
}

func oracleSet(o []orec, from, to uint32, scope uint8) []orec {
	o = oracleDelete(o, from, to)
	nf, nt := from, to
	if from > 0 {
		for i := range o {
			if o[i].to == from-1 && o[i].scope == scope {
				nf = o[i].from
				o = append(o[:i], o[i+1:]...)
				break
			}
		}
	}
	if to < ^uint32(0) {
		for i := range o {
			if o[i].from == to+1 && o[i].scope == scope {
				nt = o[i].to
				o = append(o[:i], o[i+1:]...)
				break
			}
		}
	}
	pos := 0
	for pos < len(o) && o[pos].from < nf {
		pos++
	}
	o = append(o, orec{})
	copy(o[pos+1:], o[pos:])
	o[pos] = orec{nf, nt, scope}
	return o
}

// lcg returns a deterministic generator with the same constants as the Rust oracle.
func lcg(seed uint64) func() uint32 {
	state := seed
	return func() uint32 {
		state = state*6364136223846793005 + 1442695040888963407
		return uint32(state >> 33)
	}
}

func TestOracleRandomSetDeleteV4(t *testing.T) {
	rng := lcg(0x123456789abcdef0)
	w := CreateV4(1, 0)
	var oracle []orec
	const span = 250

	for step := 0; step < 6000; step++ {
		a, b := rng()%span, rng()%span
		from, to := a, b
		if a > b {
			from, to = b, a
		}
		if rng()&1 == 0 {
			scope := uint8(rng() % 4)
			must(t, w.Set(wk(from), wk(to), []byte{scope}))
			oracle = oracleSet(oracle, from, to, scope)
		} else {
			must(t, w.Delete(wk(from), wk(to)))
			oracle = oracleDelete(oracle, from, to)
		}
		var got []orec
		w.Scan(func(f, tt Ipv4Key, s []byte) { got = append(got, orec{uint32(f), uint32(tt), s[0]}) })
		assertOrec(t, got, oracle, "v4 writer/oracle diverged at step %d", step)
		if w.RecordCount() != uint64(len(oracle)) {
			t.Fatalf("v4 count at step %d: %d != %d", step, w.RecordCount(), len(oracle))
		}
	}

	// The whole on-disk structure must pass the reader's full validation.
	w.Commit(0)
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	var got []orec
	r.ScanV4(func(f, tt Ipv4Key, s []byte) { got = append(got, orec{uint32(f), uint32(tt), s[0]}) })
	assertOrec(t, got, oracle, "v4 final scan mismatch")
	if r.RecordCount() != uint64(len(oracle)) {
		t.Fatalf("v4 final count: %d != %d", r.RecordCount(), len(oracle))
	}
}

func TestOracleRandomSetDeleteV6(t *testing.T) {
	rng := lcg(0x0fedcba987654321)
	w := CreateV6(3, 0)
	var oracle []orec
	const span = 200
	k6 := func(n uint32) Ipv6Key { return Ipv6Key{Hi: 0, Lo: uint64(n)} }

	for step := 0; step < 3000; step++ {
		a, b := rng()%span, rng()%span
		from, to := a, b
		if a > b {
			from, to = b, a
		}
		if rng()&1 == 0 {
			scope := uint8(rng() % 3)
			must(t, w.Set(k6(from), k6(to), []byte{scope, scope, scope}))
			oracle = oracleSet(oracle, from, to, scope)
		} else {
			must(t, w.Delete(k6(from), k6(to)))
			oracle = oracleDelete(oracle, from, to)
		}
		var got []orec
		w.Scan(func(f, tt Ipv6Key, s []byte) { got = append(got, orec{uint32(f.Lo), uint32(tt.Lo), s[0]}) })
		assertOrec(t, got, oracle, "v6 writer/oracle diverged at step %d", step)
		if w.RecordCount() != uint64(len(oracle)) {
			t.Fatalf("v6 count at step %d: %d != %d", step, w.RecordCount(), len(oracle))
		}
	}
	w.Commit(0)
	r, err := Open(w.Image())
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != uint64(len(oracle)) {
		t.Fatalf("v6 final count: %d != %d", r.RecordCount(), len(oracle))
	}
	var got []orec
	r.ScanV6(func(f, tt Ipv6Key, s []byte) { got = append(got, orec{uint32(f.Lo), uint32(tt.Lo), s[0]}) })
	assertOrec(t, got, oracle, "v6 final scan mismatch")
}

func assertOrec(t *testing.T, got, want []orec, format string, args ...any) {
	t.Helper()
	mismatch := len(got) != len(want)
	if !mismatch {
		for i := range got {
			if got[i] != want[i] {
				mismatch = true
				break
			}
		}
	}
	if mismatch {
		t.Fatalf(format+"\n  got  %v\n  want %v", append(args, got, want)...)
	}
}
