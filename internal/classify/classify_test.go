// internal/classify/classify_test.go
package classify

import "testing"

func TestResult_IsUsable_AllFieldsPresent(t *testing.T) {
	r := Result{
		NumeroCuenta: "AGUA-IGA-1",
		Categoria:    "agua",
		Tipo:         "factura",
		Monto:        15400.00,
		Confianza:    0.95,
	}
	if !r.IsUsable() {
		t.Fatal("expected IsUsable() to be true for a complete result")
	}
}

func TestResult_IsUsable_MissingNumeroCuenta(t *testing.T) {
	r := Result{Categoria: "agua", Tipo: "factura", Monto: 15400.00, Confianza: 0.95}
	if r.IsUsable() {
		t.Fatal("expected IsUsable() to be false: no numero_cuenta means the house can't be resolved")
	}
}

func TestResult_IsUsable_LowConfidence(t *testing.T) {
	r := Result{
		NumeroCuenta: "AGUA-IGA-1",
		Categoria:    "agua",
		Tipo:         "factura",
		Monto:        15400.00,
		Confianza:    0.40,
	}
	// Spec: low-confidence extraction must be flagged for manual review, not
	// trusted. Threshold: below 0.7 is not usable.
	if r.IsUsable() {
		t.Fatal("expected IsUsable() to be false for confianza below 0.7")
	}
}

func TestResult_IsUsable_MissingMontoOnPago(t *testing.T) {
	r := Result{NumeroCuenta: "AGUA-IGA-1", Categoria: "agua", Tipo: "pago", Monto: 0, Confianza: 0.95}
	if r.IsUsable() {
		t.Fatal("expected IsUsable() to be false: pago with no monto can't be matched")
	}
}
