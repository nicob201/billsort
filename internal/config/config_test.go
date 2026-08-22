// internal/config/config_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testJSON = `{
  "casas": [
    {
      "nombre": "Igarzabal",
      "categorias": {
        "agua": {"numero_cuenta": "AGUA-IGA-1"},
        "gas": {"numero_cuenta": "GAS-IGA-1"},
        "luz": {"numero_cuenta": "LUZ-IGA-1"}
      }
    },
    {
      "nombre": "La Granja",
      "categorias": {
        "agua": {"numero_cuenta": "AGUA-GRA-1"},
        "luz": {"numero_cuenta": "LUZ-GRA-1"}
      }
    }
  ]
}`

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "houses.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	return path
}

func TestLoad_ValidFile(t *testing.T) {
	path := writeTestFile(t, testJSON)
	h, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(h.Casas) != 2 {
		t.Fatalf("expected 2 casas, got %d", len(h.Casas))
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/houses.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestFindCasa_KnownAccount(t *testing.T) {
	path := writeTestFile(t, testJSON)
	h, _ := Load(path)

	nombre, ok := h.FindCasa("agua", "AGUA-IGA-1")
	if !ok || nombre != "Igarzabal" {
		t.Fatalf("expected (Igarzabal, true), got (%q, %v)", nombre, ok)
	}
}

func TestFindCasa_UnknownAccount(t *testing.T) {
	path := writeTestFile(t, testJSON)
	h, _ := Load(path)

	_, ok := h.FindCasa("agua", "NO-EXISTE")
	if ok {
		t.Fatal("expected ok=false for unknown account number")
	}
}

func TestFindCasa_CategoryNotPresentForHouse(t *testing.T) {
	// La Granja has no "gas" category — must not false-match.
	path := writeTestFile(t, testJSON)
	h, _ := Load(path)

	_, ok := h.FindCasa("gas", "AGUA-GRA-1")
	if ok {
		t.Fatal("expected ok=false: La Granja has no gas category")
	}
}

func TestCategoriasDe(t *testing.T) {
	path := writeTestFile(t, testJSON)
	h, _ := Load(path)

	got := h.CategoriasDe("La Granja")
	want := map[string]bool{"agua": true, "luz": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d categorias, got %d (%v)", len(want), len(got), got)
	}
	for _, c := range got {
		if !want[c] {
			t.Fatalf("unexpected categoria %q", c)
		}
	}
}
