// internal/config/config.go

// Package config loads the houses.json file that maps each house's fixed
// utility account numbers to its name and categories. It exists so the rest
// of billsort can resolve a document's numero_cuenta to a house without any
// hardcoded secrets in source.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Categoria holds the data billsort needs about one utility category
// ("agua", "gas", "luz") of one house.
type Categoria struct {
	// NumeroCuenta is the fixed account/client number printed on every bill
	// for this house+category. It uniquely identifies the house.
	NumeroCuenta string `json:"numero_cuenta"`
}

// Casa is one house: its display name plus the categories it actually has,
// keyed by category name. A house may list fewer than all three categories
// (e.g. La Granja has agua and luz only).
type Casa struct {
	Nombre     string               `json:"nombre"`
	Categorias map[string]Categoria `json:"categorias"`
}

// Houses is the root of houses.json: the list of all configured houses.
type Houses struct {
	Casas []Casa `json:"casas"`
}

// Load reads and parses the houses config at path. It returns an error if
// the file cannot be read or is not valid JSON.
func Load(path string) (*Houses, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading houses config %q: %w", path, err)
	}
	var h Houses
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parsing houses config %q: %w", path, err)
	}
	return &h, nil
}

// FindCasa returns the house name that owns the given account number for the
// given category, or ok=false if no house has that (categoria, numeroCuenta) pair.
func (h *Houses) FindCasa(categoria, numeroCuenta string) (string, bool) {
	for _, casa := range h.Casas {
		cat, present := casa.Categorias[categoria]
		if present && cat.NumeroCuenta == numeroCuenta {
			return casa.Nombre, true
		}
	}
	return "", false
}

// CategoriasDe returns the category keys (e.g. "agua", "gas", "luz") that
// the named house actually has, in no particular order.
func (h *Houses) CategoriasDe(nombreCasa string) []string {
	for _, casa := range h.Casas {
		if casa.Nombre == nombreCasa {
			out := make([]string, 0, len(casa.Categorias))
			for k := range casa.Categorias {
				out = append(out, k)
			}
			return out
		}
	}
	return nil
}
