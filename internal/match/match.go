// internal/match/match.go

// Package match holds billsort's pure naming and pairing logic: computing
// the next NN load counter for a Drive folder, building filenames per the
// existing convention, and matching a payment receipt ("pago") to its pending
// factura by exact amount. It has no network dependency so all of it is
// unit-testable without hitting live APIs.
package match

import "fmt"

// PendingFactura is one factura already in Drive that doesn't have a paired
// "pago" file yet — a candidate to be matched against an incoming pago.
type PendingFactura struct {
	NN            int     // the zero-padded load counter from the filename
	NumeroFactura string  // the utility's own invoice number
	Monto         float64 // amount in ARS, re-read from the factura itself
}

// MatchStatus is the outcome kind of a pago-matching attempt.
type MatchStatus int

const (
	MatchNone      MatchStatus = iota // no pending factura has that amount
	MatchExact                        // exactly one pending factura matches
	MatchAmbiguous                    // multiple match — caller must ask the user
)

// MatchOutcome is the result of FindPagoMatch. Which fields are meaningful
// depends on Status: Match when Status == MatchExact, Candidates when
// Status == MatchAmbiguous.
type MatchOutcome struct {
	Status     MatchStatus
	Match      PendingFactura   // valid when Status == MatchExact
	Candidates []PendingFactura // valid when Status == MatchAmbiguous
}

// NextNN returns the next load-counter value for a Drive folder given the NN
// values already present there. Uses max+1, not count+1, because real folders
// have gaps (a house's history can skip numbers).
func NextNN(existing []int) int {
	max := 0
	for _, n := range existing {
		if n > max {
			max = n
		}
	}
	return max + 1
}

// BuildFilename reproduces the existing Drive naming convention:
// "NN-<numeroFactura>[ pago].ext", NN zero-padded to at least 2 digits.
func BuildFilename(nn int, numeroFactura, tipo, ext string) string {
	suffix := ""
	if tipo == "pago" {
		suffix = " pago"
	}
	return fmt.Sprintf("%02d-%s%s.%s", nn, numeroFactura, suffix, ext)
}

// FindPagoMatch looks for exactly one pending factura whose amount exactly
// equals monto (no tolerance — see spec). Zero or multiple matches must be
// resolved by the caller (interactive prompt), never guessed.
func FindPagoMatch(pending []PendingFactura, monto float64) MatchOutcome {
	var matches []PendingFactura
	for _, p := range pending {
		if p.Monto == monto {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return MatchOutcome{Status: MatchNone}
	case 1:
		return MatchOutcome{Status: MatchExact, Match: matches[0]}
	default:
		return MatchOutcome{Status: MatchAmbiguous, Candidates: matches}
	}
}
