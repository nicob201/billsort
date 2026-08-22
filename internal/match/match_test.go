// internal/match/match_test.go
package match

import "testing"

func TestNextNN_Empty(t *testing.T) {
	if got := NextNN(nil); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
}

func TestNextNN_Sequential(t *testing.T) {
	if got := NextNN([]int{1, 2, 3}); got != 4 {
		t.Fatalf("expected 4, got %d", got)
	}
}

func TestNextNN_WithGap(t *testing.T) {
	// La Granja's real Drive folder: NN 02 then 06, nothing in between.
	if got := NextNN([]int{2, 6}); got != 7 {
		t.Fatalf("expected 7 (max+1, gap-safe), got %d", got)
	}
}

func TestBuildFilename_Factura(t *testing.T) {
	got := BuildFilename(3, "FC-0010-71103571", "factura", "pdf")
	want := "03-FC-0010-71103571.pdf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildFilename_Pago(t *testing.T) {
	got := BuildFilename(3, "FC-0010-71103571", "pago", "jpeg")
	want := "03-FC-0010-71103571 pago.jpeg"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildFilename_ZeroPadsToAtLeastTwoDigits(t *testing.T) {
	got := BuildFilename(9, "0400B74211857A", "factura", "pdf")
	want := "09-0400B74211857A.pdf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildFilename_ThreeDigitNNNotTruncated(t *testing.T) {
	got := BuildFilename(123, "X", "factura", "pdf")
	want := "123-X.pdf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFindPagoMatch_ExactlyOneMatch(t *testing.T) {
	pending := []PendingFactura{
		{NN: 8, NumeroFactura: "FC-1", Monto: 15400.00},
		{NN: 9, NumeroFactura: "FC-2", Monto: 16200.50},
	}
	outcome := FindPagoMatch(pending, 16200.50)

	if outcome.Status != MatchExact {
		t.Fatalf("expected MatchExact, got %v", outcome.Status)
	}
	if outcome.Match.NN != 9 {
		t.Fatalf("expected NN 9, got %d", outcome.Match.NN)
	}
}

func TestFindPagoMatch_NoMatch(t *testing.T) {
	pending := []PendingFactura{{NN: 8, NumeroFactura: "FC-1", Monto: 15400.00}}
	outcome := FindPagoMatch(pending, 999.99)

	if outcome.Status != MatchNone {
		t.Fatalf("expected MatchNone, got %v", outcome.Status)
	}
}

func TestFindPagoMatch_AmbiguousSameAmount(t *testing.T) {
	// Real scenario from the spec: current month's and next month's facturas
	// both pending at once, happen to share an amount.
	pending := []PendingFactura{
		{NN: 8, NumeroFactura: "FC-1", Monto: 15400.00},
		{NN: 9, NumeroFactura: "FC-2", Monto: 15400.00},
	}
	outcome := FindPagoMatch(pending, 15400.00)

	if outcome.Status != MatchAmbiguous {
		t.Fatalf("expected MatchAmbiguous, got %v", outcome.Status)
	}
	if len(outcome.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(outcome.Candidates))
	}
}

func TestFindPagoMatch_NoToleranceOnAmount(t *testing.T) {
	pending := []PendingFactura{{NN: 8, NumeroFactura: "FC-1", Monto: 15400.00}}
	outcome := FindPagoMatch(pending, 15400.01)

	if outcome.Status != MatchNone {
		t.Fatalf("spec requires exact match, no tolerance — expected MatchNone, got %v", outcome.Status)
	}
}
