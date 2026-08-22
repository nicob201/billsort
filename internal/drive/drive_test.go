// internal/drive/drive_test.go
package drive

import "testing"

func TestParseNN_ValidFactura(t *testing.T) {
	nn, ok := ParseNN("03-FC-0010-71103571.pdf")
	if !ok || nn != 3 {
		t.Fatalf("expected (3, true), got (%d, %v)", nn, ok)
	}
}

func TestParseNN_ValidPago(t *testing.T) {
	nn, ok := ParseNN("09-0400B74211857A pago.jpeg")
	if !ok || nn != 9 {
		t.Fatalf("expected (9, true), got (%d, %v)", nn, ok)
	}
}

func TestParseNN_ThreeDigits(t *testing.T) {
	nn, ok := ParseNN("123-X.pdf")
	if !ok || nn != 123 {
		t.Fatalf("expected (123, true), got (%d, %v)", nn, ok)
	}
}

func TestParseNN_NoLeadingNumber(t *testing.T) {
	_, ok := ParseNN("Cedulon-deuda-consolidada-tributario-2026.pdf")
	if ok {
		t.Fatal("expected ok=false for a filename with no NN- prefix")
	}
}
