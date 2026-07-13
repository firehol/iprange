package iprangedb

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"testing"
)

//go:embed conformance_cases.json
var casesJSON []byte

// Conformance test: shared behavioral cases from v4/conformance/cases.json.
// Compares merged interval coverage (not exact record count).
// This is the cross-language correctness anchor — the same cases.json
// is used by the Rust conformance test.

func parseU32(s string) uint32 {
	var v uint32
	for _, c := range s {
		v = v*10 + uint32(c-'0')
	}
	return v
}

func scopeB2U(b []byte) uint32 {
	var buf [4]byte
	for i := 0; i < len(b) && i < 4; i++ {
		buf[i] = b[i]
	}
	return uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24
}

type confScan struct {
	From, To, Scope uint32
}

func mergeAdjacent(recs []confScan) []confScan {
	if len(recs) == 0 {
		return nil
	}
	out := []confScan{recs[0]}
	for _, r := range recs[1:] {
		last := &out[len(out)-1]
		if r.Scope == last.Scope && r.From == last.To+1 {
			last.To = r.To
		} else {
			out = append(out, r)
		}
	}
	return out
}

func scanEqual(a, b []confScan) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestConformance(t *testing.T) {
	var raw []struct {
		Name   string `json:"name"`
		Family string `json:"family"`
		Ops    []struct {
			Op    string `json:"op"`
			From  string `json:"from"`
			To    string `json:"to"`
			Scope []byte `json:"scope"`
		} `json:"ops"`
		ExpectScan [][]json.RawMessage `json:"expect_scan"`
		ExpectLookup []json.RawMessage `json:"expect_lookup"`
	}
	if err := json.Unmarshal(casesJSON, &raw); err != nil {
		t.Fatalf("parse cases.json: %v", err)
	}

	for _, r := range raw {
		if r.Family != "v4" {
			continue
		}
		t.Run(r.Name, func(t *testing.T) {
			w, err := Create[Ipv4Key](ScopeModeScalar, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range r.Ops {
				from := Ipv4Key(parseU32(op.From))
				to := Ipv4Key(parseU32(op.To))
				switch op.Op {
				case "set":
					if err := w.Set(from, to, scopeB2U(op.Scope)); err != nil {
						t.Fatalf("Set: %v", err)
					}
				case "delete":
					if _, err := w.Delete(from, to); err != nil {
						t.Fatalf("Delete: %v", err)
					}
				}
			}
			if err := w.Commit(0, maxUint64); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			img, _ := w.IntoImage()
			reader, err := Open(img)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}

			// Verify scan (merged interval comparison).
			var got []confScan
			reader.ScanV4(func(from, to Ipv4Key, scopeID uint32) {
				got = append(got, confScan{
					From: uint32(from), To: uint32(to), Scope: scopeID,
				})
			})
			gotMerged := mergeAdjacent(got)

			var want []confScan
			for _, s := range r.ExpectScan {
				var fromStr, toStr string
				var scopeBytes []byte
				json.Unmarshal(s[0], &fromStr)
				json.Unmarshal(s[1], &toStr)
				json.Unmarshal(s[2], &scopeBytes)
				want = append(want, confScan{
					From: parseU32(fromStr), To: parseU32(toStr),
					Scope: scopeB2U(scopeBytes),
				})
			}
			if !scanEqual(gotMerged, want) {
				t.Errorf("scan mismatch:\n  got:  %v\n  want: %v", gotMerged, want)
			}

			// Verify lookups.
			for _, raw := range r.ExpectLookup {
				var pair [2]json.RawMessage
				if err := json.Unmarshal(raw, &pair); err != nil {
					t.Fatalf("parse lookup entry: %v", err)
				}
				var ipStr string
				if err := json.Unmarshal(pair[0], &ipStr); err != nil {
					// might be a number
					var ipNum float64
					if err := json.Unmarshal(pair[0], &ipNum); err != nil {
						continue
					}
					ipStr = strconv.Itoa(int(ipNum))
				}
				ip := parseU32(ipStr)
				gotID, ok := reader.LookupV4(Ipv4Key(ip))
				if string(pair[1]) == "null" {
					if ok {
						t.Errorf("lookup(%d): expected miss, got scope %d", ip, gotID)
					}
				} else {
					var scopeBytes []byte
					if err := json.Unmarshal(pair[1], &scopeBytes); err != nil {
						t.Fatalf("parse scope: %v", err)
					}
					want := scopeB2U(scopeBytes)
					if !ok {
						t.Errorf("lookup(%d): expected scope %d, got miss", ip, want)
					} else if gotID != want {
						t.Errorf("lookup(%d): got scope %d, want %d", ip, gotID, want)
					}
				}
			}
		})
	}
}
