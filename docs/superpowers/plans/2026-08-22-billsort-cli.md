# billsort CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Deviation from normal execution for this project:** implementation is NOT done by a Claude subagent. The user pairs with DeepSeek (via OpenCode, in VS Code) to write and apply every line of code. Claude's role per task is to hand the user an English prompt for DeepSeek plus the recommended git branch name, then review the resulting `git diff` with the user before moving to the next task. Claude does not create branches, does not commit, and does not run build/lint/test commands unless the user is stuck and explicitly asks for one to be run.

**Goal:** A Go CLI (`billsort <local-folder>`) that classifies tax/utility documents dropped in a local folder via the Gemini API, computes the correct filename and Google Drive destination per the existing convention, and uploads them after a single confirmation.

**Architecture:** Four small internal packages behind narrow interfaces (`config`, `match`, `drive`, `classify`) wired together by `main.go`. `match` is pure logic (no network) and carries the real test weight; `drive` and `classify` are thin API wrappers consumed through interfaces so `main`'s orchestration is testable with fakes.

**Tech Stack:** Go (stdlib only where possible — no CLI framework, no YAML library), Gemini API (multimodal PDF/JPG/PNG input, exact package TBD-at-implementation-time — see Task 4), Google Drive API v3 (`google.golang.org/api/drive/v3` + `golang.org/x/oauth2/google`).

**Spec:** `docs/superpowers/specs/2026-08-22-billsort-design.md`

## Global Constraints

- No CLI framework (Cobra/Viper) — single command, stdlib `os.Args` is enough.
- No local database/cache — every run re-lists the live Drive folder. (Spec: "Rejected alternatives.")
- Config format is **JSON, not YAML** — deviates from the spec's `houses.yaml` naming, decided during planning to avoid pulling in a YAML dependency for a config this small (`encoding/json` is stdlib). Files are `houses.json` (real, gitignored) / `houses.example.json` (template, committed). Functionally identical to what the spec describes — same 11 account numbers, same per-house category sets.
- `houses.json`, Drive OAuth credentials/token cache, and the Gemini API key are **never committed** — repo is public. `.gitignore` must cover all three from Task 1 onward.
- Filename convention: `NN-<numero_factura>[ pago].<ext>`, `NN` zero-padded to 2 digits minimum, computed as **max existing NN in the target Drive folder + 1**.
- Matching a loose "pago" file to a pending factura: exact amount match only, no tolerance. Zero or multiple candidates → surface to the user for interactive resolution, never guess.
- Houses/categories: Igarzabal, Pardos y Morenos, Yaco Misque (agua, gas, luz each); La Granja (agua, luz only — no gas).
- Every step that touches network (Drive, Gemini) must go through the `drive.Client` / `classify.Classifier` interfaces defined in Task 3/4 — never called directly from `main` or from `match`.

---

## Task 1: Project scaffolding + `config` package

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `houses.example.json`

**Interfaces:**
- Produces: `config.Houses` struct, `config.Load(path string) (*Houses, error)`, `(*Houses).FindCasa(categoria, numeroCuenta string) (nombreCasa string, ok bool)`, `(*Houses).CategoriasDe(nombreCasa string) []string`

- [ ] **Step 1: Initialize the Go module**

Run: `go mod init github.com/nicob201/billsort`
Expected: creates `go.mod` with `module github.com/nicob201/billsort` and a `go` directive matching the installed Go version.

- [ ] **Step 2: Write `.gitignore`**

```gitignore
houses.json
credentials.json
token.json
.env
```

- [ ] **Step 3: Write the failing test for `config`**

```go
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
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/config/...`
Expected: FAIL — package `config` and its types don't exist yet.

- [ ] **Step 5: Implement `config.go`**

```go
// internal/config/config.go
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Categoria struct {
	NumeroCuenta string `json:"numero_cuenta"`
}

type Casa struct {
	Nombre     string               `json:"nombre"`
	Categorias map[string]Categoria `json:"categorias"`
}

type Houses struct {
	Casas []Casa `json:"casas"`
}

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
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/config/...`
Expected: PASS, all 6 tests.

- [ ] **Step 7: Write `houses.example.json`**

```json
{
  "casas": [
    {
      "nombre": "Igarzabal",
      "categorias": {
        "agua": {"numero_cuenta": "REEMPLAZAR"},
        "gas": {"numero_cuenta": "REEMPLAZAR"},
        "luz": {"numero_cuenta": "REEMPLAZAR"}
      }
    },
    {
      "nombre": "Pardos y Morenos",
      "categorias": {
        "agua": {"numero_cuenta": "REEMPLAZAR"},
        "gas": {"numero_cuenta": "REEMPLAZAR"},
        "luz": {"numero_cuenta": "REEMPLAZAR"}
      }
    },
    {
      "nombre": "Yaco Misque",
      "categorias": {
        "agua": {"numero_cuenta": "REEMPLAZAR"},
        "gas": {"numero_cuenta": "REEMPLAZAR"},
        "luz": {"numero_cuenta": "REEMPLAZAR"}
      }
    },
    {
      "nombre": "La Granja",
      "categorias": {
        "agua": {"numero_cuenta": "REEMPLAZAR"},
        "luz": {"numero_cuenta": "REEMPLAZAR"}
      }
    }
  ]
}
```

- [ ] **Step 8: Commit**

```bash
git add go.mod .gitignore internal/config houses.example.json
git commit -m "feat: add houses config loader"
```

---

## Task 2: `match` package — NN computation + pairing logic (pure)

**Files:**
- Create: `internal/match/match.go`
- Create: `internal/match/match_test.go`

**Interfaces:**
- Consumes: nothing (pure package, no dependency on config/drive/classify)
- Produces: `match.NextNN(existing []int) int`, `match.BuildFilename(nn int, numeroFactura, tipo, ext string) string`, `match.FindPagoMatch(pending []PendingFactura, monto float64) MatchOutcome`, types `PendingFactura`, `MatchOutcome`, `MatchStatus` (const: `MatchNone`, `MatchExact`, `MatchAmbiguous`)

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/match/...`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Implement `match.go`**

```go
// internal/match/match.go
package match

import "fmt"

type PendingFactura struct {
	NN            int
	NumeroFactura string
	Monto         float64
}

type MatchStatus int

const (
	MatchNone MatchStatus = iota
	MatchExact
	MatchAmbiguous
)

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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/match/...`
Expected: PASS, all 11 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/match
git commit -m "feat: add NN computation and factura/pago matching logic"
```

---

## Task 3: `drive` package — Google Drive client

**Files:**
- Create: `internal/drive/drive.go`
- Create: `internal/drive/drive_test.go`
- Modify: `go.mod` (adds `google.golang.org/api` and `golang.org/x/oauth2`)

**Interfaces:**
- Consumes: nothing from earlier tasks
- Produces: `drive.FileEntry` struct (`Name`, `ID string`), `drive.Client` interface with `ListFiles(ctx, pathSegments []string) ([]FileEntry, error)`, `EnsureFolder(ctx, pathSegments []string) (folderID string, err error)`, `Upload(ctx, folderID, filename, localPath string) error`, `Download(ctx, fileID, destLocalPath string) error`; `drive.ParseNN(filename string) (nn int, ok bool)`; concrete constructor `drive.NewClient(ctx, credentialsPath, tokenCachePath string) (Client, error)`

`Download` exists so Task 5 can pull a pending factura's content back down and re-run it through `classify.Classifier` to recover its `Monto` — Drive stores filenames, not amounts, so matching a loose "pago" file to a pending factura requires re-reading the factura itself.

**Before implementing:** `google.golang.org/api/drive/v3` and `golang.org/x/oauth2/google` are stable, well-documented packages, but do not guess exact method chains from memory — check https://pkg.go.dev/google.golang.org/api/drive/v3 for the current `Files.List`, `Files.Create` (folder creation via `mimeType: "application/vnd.google-apps.folder"`), and media-upload signatures before writing the real client, then compile-and-fix against any drift.

- [ ] **Step 1: Write the failing test for the pure part (`ParseNN`)**

```go
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
```

Run: `go test ./internal/drive/... -run TestParseNN`
Expected: FAIL — `drive` package doesn't exist yet.

- [ ] **Step 2: Implement `Client` interface, `ParseNN`, and the real Drive-backed implementation**

```go
// internal/drive/drive.go
package drive

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FileEntry is one file's name and Drive file ID, as returned by ListFiles.
type FileEntry struct {
	Name string
	ID   string
}

// Client is the seam between billsort's orchestration logic and the real
// Google Drive API — lets main.go's flow be tested with a fake.
type Client interface {
	// ListFiles returns every file currently in the folder identified by
	// pathSegments (e.g. []string{"2026", "Igarzabal", "Agua"}). Returns an
	// empty slice, not an error, if the folder doesn't exist yet.
	ListFiles(ctx context.Context, pathSegments []string) ([]FileEntry, error)

	// EnsureFolder creates any missing segment of pathSegments and returns the
	// leaf folder's Drive file ID.
	EnsureFolder(ctx context.Context, pathSegments []string) (folderID string, err error)

	// Upload uploads the contents of localPath into the folder identified by
	// folderID, naming it filename.
	Upload(ctx context.Context, folderID, filename, localPath string) error

	// Download saves the Drive file identified by fileID to destLocalPath.
	// Used to re-read a pending factura's content (Task 5) since Drive only
	// stores filenames, not the amount billsort needs for pago matching.
	Download(ctx context.Context, fileID, destLocalPath string) error
}

// ParseNN extracts the leading "NN-" load-counter from a billsort-convention
// filename. ok is false for filenames that don't start with digits followed
// by a hyphen (e.g. the untouched Rentas/Fiat Toro filenames this tool never
// generates or reads as NN-bearing).
func ParseNN(filename string) (int, bool) {
	i := strings.IndexByte(filename, '-')
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(filename[:i])
	if err != nil {
		return 0, false
	}
	return n, true
}

// driveClient is the real Client implementation backed by the Drive API v3.
// NOTE for implementer: verify exact google.golang.org/api/drive/v3 method
// signatures against current docs before finalizing — this is a sketch of
// the required shape, not a guarantee of the current SDK's exact call chain.
type driveClient struct {
	// svc *drive.Service (google.golang.org/api/drive/v3) — wire up in
	// NewClient via the OAuth2 flow below.
}

// NewClient builds a Drive client using an OAuth2 "installed app" flow:
// reads the OAuth client secret at credentialsPath, reuses a cached token
// from tokenCachePath if present and valid, otherwise opens a browser for
// one-time user consent and writes the resulting token to tokenCachePath.
func NewClient(ctx context.Context, credentialsPath, tokenCachePath string) (Client, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *driveClient) ListFiles(ctx context.Context, pathSegments []string) ([]FileEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *driveClient) EnsureFolder(ctx context.Context, pathSegments []string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (c *driveClient) Upload(ctx context.Context, folderID, filename, localPath string) error {
	return fmt.Errorf("not implemented")
}

func (c *driveClient) Download(ctx context.Context, fileID, destLocalPath string) error {
	return fmt.Errorf("not implemented")
}
```

- [ ] **Step 3: Run `ParseNN` tests to verify they pass**

Run: `go test ./internal/drive/... -run TestParseNN`
Expected: PASS, all 4 tests.

- [ ] **Step 4: Replace the `not implemented` bodies with real Drive API calls**

Implement `NewClient`, `ListFiles`, `EnsureFolder`, and `Upload` against `google.golang.org/api/drive/v3` and `golang.org/x/oauth2/google`, per the doc lookup noted above:
- `ListFiles`: resolve `pathSegments` to a folder ID by walking `Files.List` queries (`'<parentID>' in parents and mimeType = 'application/vnd.google-apps.folder' and name = '<segment>'`) one segment at a time; if any segment is missing, return `nil, nil` (empty, not an error). Once the leaf folder ID is found, list its non-folder children's `id` and `name` fields into `[]FileEntry`.
- `EnsureFolder`: same walk, but create any missing segment via `Files.Create` with `mimeType: "application/vnd.google-apps.folder"` and the right `Parents`, instead of returning empty.
- `Upload`: `Files.Create` with `Name: filename`, `Parents: []string{folderID}`, and the file content attached as media (multipart upload) from `localPath`.
- `Download`: `Files.Get(fileID).Download()` (or the current equivalent — verify against docs), writing the response body to `destLocalPath`.
- Use the `drive.file` OAuth scope only (per spec — minimal access, limited to files/folders this app creates).

This step has no isolated unit test (it requires live Drive credentials) — it gets verified manually end-to-end once Task 5 wires everything together and the user runs the real CLI against their own Drive.

- [ ] **Step 5: Commit**

```bash
git add internal/drive go.mod go.sum
git commit -m "feat: add Google Drive client"
```

---

## Task 4: `classify` package — Gemini extraction client

**Files:**
- Create: `internal/classify/classify.go`
- Create: `internal/classify/classify_test.go`
- Modify: `go.mod` (adds the Gemini Go SDK)

**Interfaces:**
- Consumes: nothing from earlier tasks
- Produces: `classify.Result` struct (`NumeroCuenta`, `Categoria`, `Tipo`, `Monto`, `NumeroFactura`, `Confianza string/float64` — see field list below), `classify.Classifier` interface with `Classify(ctx, localPath string) (Result, error)`, concrete constructor `classify.NewClient(ctx, apiKey string) (Classifier, error)`

**Before implementing:** the current official Go SDK package for the Gemini API has changed names before (older `github.com/google/generative-ai-go/genai` vs. the newer unified `google.golang.org/genai`). Check https://ai.google.dev/gemini-api/docs/libraries for the currently-recommended Go package before running `go get` — do not assume either name from memory.

- [ ] **Step 1: Write the failing test for the pure part (`Result` validation)**

```go
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
```

Run: `go test ./internal/classify/... -run TestResult`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 2: Implement `Result`, `IsUsable`, and the `Classifier` interface**

```go
// internal/classify/classify.go
package classify

import (
	"context"
	"fmt"
)

// Result is the structured extraction billsort needs from one document.
type Result struct {
	NumeroCuenta  string  // fixed account/client number printed on the bill
	Categoria     string  // "agua" | "gas" | "luz"
	Tipo          string  // "factura" | "pago"
	Monto         float64 // amount in ARS
	NumeroFactura string  // the utility's own invoice number, when present
	Confianza     float64 // model's self-reported confidence, 0.0-1.0
}

// IsUsable reports whether Result has everything billsort needs to act on
// automatically. Per spec: low-confidence or incomplete extraction must be
// flagged for manual review, never guessed through.
func (r Result) IsUsable() bool {
	const minConfianza = 0.7
	if r.Confianza < minConfianza {
		return false
	}
	if r.NumeroCuenta == "" || r.Categoria == "" || r.Tipo == "" {
		return false
	}
	if r.Monto <= 0 {
		return false
	}
	return true
}

// Classifier extracts a Result from one local document (PDF, JPG, JPEG, or
// PNG). Behind an interface so main.go's orchestration is testable with a
// fake, without live Gemini calls.
type Classifier interface {
	Classify(ctx context.Context, localPath string) (Result, error)
}

// geminiClassifier is the real Classifier backed by the Gemini API.
// NOTE for implementer: confirm the current Go SDK package (see doc link
// above) before wiring this up — do not assume the import path.
type geminiClassifier struct {
	// client *genai.Client (exact type depends on the confirmed SDK package)
}

func NewClient(ctx context.Context, apiKey string) (Classifier, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *geminiClassifier) Classify(ctx context.Context, localPath string) (Result, error) {
	return Result{}, fmt.Errorf("not implemented")
}
```

- [ ] **Step 3: Run `Result` tests to verify they pass**

Run: `go test ./internal/classify/... -run TestResult`
Expected: PASS, all 4 tests.

- [ ] **Step 4: Implement `NewClient` and `Classify` against the real Gemini API**

Requirements for the prompt sent to Gemini:
- Attach the file at `localPath` as multimodal input (PDF via document input, JPG/PNG/JPEG via image input — media type inferred from the file extension).
- Request **structured JSON output** matching the `Result` fields: `numero_cuenta`, `categoria` (must be one of `"agua"`, `"gas"`, `"luz"`), `tipo` (`"factura"` or `"pago"`), `monto` (number), `numero_factura` (string, may be empty), `confianza` (0.0-1.0, the model's own confidence in this extraction).
- The prompt must explicitly instruct the model: if a field can't be confidently read from the document, set `confianza` low rather than fabricate a value — `IsUsable()` (Task 4 Step 2) is what turns that into a "needs manual review" skip in Task 5.
- Parse the JSON response into `Result` and return it; a parse failure or an empty response is an `error`, not a zero-value `Result`.

No isolated unit test for this step (needs a live API key) — verified manually once Task 5 wires the real CLI end-to-end.

- [ ] **Step 5: Commit**

```bash
git add internal/classify go.mod go.sum
git commit -m "feat: add Gemini document classifier"
```

---

## Task 5: `main.go` — CLI orchestration

**Files:**
- Create: `main.go`
- Create: `main_test.go`

**Interfaces:**
- Consumes: `config.Load`, `config.Houses.FindCasa`, `config.Houses.CategoriasDe` (Task 1); `match.NextNN`, `match.BuildFilename`, `match.FindPagoMatch`, `match.PendingFactura`, `match.MatchStatus` (Task 2); `drive.Client`, `drive.ParseNN` (Task 3); `classify.Classifier`, `classify.Result` (Task 4)
- Produces: `planBatch(ctx, files []string, cfg *config.Houses, cl classify.Classifier, dr drive.Client, year string) (BatchPlan, error)` — the orchestration core, deliberately separated from `main()` so it's testable with fakes; a `BatchPlan` struct holding the per-file planned actions plus files that need manual review or interactive disambiguation.

- [ ] **Step 1: Write the failing test for `planBatch` using fakes**

```go
// main_test.go
package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/nicob201/billsort/internal/classify"
	"github.com/nicob201/billsort/internal/config"
	"github.com/nicob201/billsort/internal/drive"
)

// fakeClassifier answers Classify() two ways: `results` for files passed
// directly into planBatch's input list (keyed by that literal local path),
// and `byFileID` for pending facturas that planBatch downloads from Drive
// first (keyed by the Drive file ID). `downloads` is a map SHARED with the
// fakeDrive in the same test (same map value passed to both) — fakeDrive.
// Download() records destLocalPath -> fileID there, so this lookup can
// resolve a downloaded temp path back to the fileID it came from.
type fakeClassifier struct {
	results   map[string]classify.Result
	byFileID  map[string]classify.Result
	downloads map[string]string
}

func (f fakeClassifier) Classify(ctx context.Context, localPath string) (classify.Result, error) {
	if r, ok := f.results[localPath]; ok {
		return r, nil
	}
	if fileID, ok := f.downloads[localPath]; ok {
		if r, ok := f.byFileID[fileID]; ok {
			return r, nil
		}
	}
	return classify.Result{}, fmt.Errorf("fake classifier: no result configured for %q", localPath)
}

type fakeDrive struct {
	files     map[string][]drive.FileEntry // key: strings.Join(pathSegments, "/")
	downloads map[string]string            // destLocalPath -> fileID; share this exact map with fakeClassifier
}

func (f fakeDrive) ListFiles(ctx context.Context, pathSegments []string) ([]drive.FileEntry, error) {
	return f.files[joinPath(pathSegments)], nil
}
func (f fakeDrive) EnsureFolder(ctx context.Context, pathSegments []string) (string, error) {
	return joinPath(pathSegments), nil
}
func (f fakeDrive) Upload(ctx context.Context, folderID, filename, localPath string) error {
	return nil
}
func (f fakeDrive) Download(ctx context.Context, fileID, destLocalPath string) error {
	f.downloads[destLocalPath] = fileID
	return nil
}

func joinPath(segs []string) string {
	out := ""
	for i, s := range segs {
		if i > 0 {
			out += "/"
		}
		out += s
	}
	return out
}

func testHouses(t *testing.T) *config.Houses {
	t.Helper()
	h := &config.Houses{Casas: []config.Casa{
		{Nombre: "Igarzabal", Categorias: map[string]config.Categoria{
			"agua": {NumeroCuenta: "AGUA-IGA-1"},
		}},
	}}
	return h
}

func TestPlanBatch_NewFactura(t *testing.T) {
	cfg := testHouses(t)
	cl := fakeClassifier{results: map[string]classify.Result{
		"/local/f1.pdf": {
			NumeroCuenta: "AGUA-IGA-1", Categoria: "agua", Tipo: "factura",
			Monto: 15400.00, NumeroFactura: "FC-1", Confianza: 0.95,
		},
	}, downloads: map[string]string{}}
	dr := fakeDrive{files: map[string][]drive.FileEntry{
		"2026/Igarzabal/agua": {{Name: "01-FC-0.pdf", ID: "drive-file-0"}},
	}, downloads: map[string]string{}}

	plan, err := planBatch(context.Background(), []string{"/local/f1.pdf"}, cfg, cl, dr, "2026")
	if err != nil {
		t.Fatalf("planBatch returned error: %v", err)
	}
	if len(plan.Uploads) != 1 {
		t.Fatalf("expected 1 planned upload, got %d", len(plan.Uploads))
	}
	got := plan.Uploads[0]
	if got.TargetFilename != "02-FC-1.pdf" {
		t.Fatalf("expected filename 02-FC-1.pdf, got %q", got.TargetFilename)
	}
	if got.TargetPath != "2026/Igarzabal/agua" {
		t.Fatalf("expected path 2026/Igarzabal/agua, got %q", got.TargetPath)
	}
}

func TestPlanBatch_LowConfidenceNeedsReview(t *testing.T) {
	cfg := testHouses(t)
	cl := fakeClassifier{results: map[string]classify.Result{
		"/local/f2.pdf": {
			NumeroCuenta: "AGUA-IGA-1", Categoria: "agua", Tipo: "factura",
			Monto: 15400.00, NumeroFactura: "FC-2", Confianza: 0.30,
		},
	}, downloads: map[string]string{}}
	dr := fakeDrive{files: map[string][]drive.FileEntry{}, downloads: map[string]string{}}

	plan, err := planBatch(context.Background(), []string{"/local/f2.pdf"}, cfg, cl, dr, "2026")
	if err != nil {
		t.Fatalf("planBatch returned error: %v", err)
	}
	if len(plan.Uploads) != 0 {
		t.Fatalf("expected 0 planned uploads, got %d", len(plan.Uploads))
	}
	if len(plan.NeedsReview) != 1 {
		t.Fatalf("expected 1 file needing review, got %d", len(plan.NeedsReview))
	}
}

func TestPlanBatch_UnknownAccountNeedsReview(t *testing.T) {
	cfg := testHouses(t)
	cl := fakeClassifier{results: map[string]classify.Result{
		"/local/f3.pdf": {
			NumeroCuenta: "NO-EXISTE", Categoria: "agua", Tipo: "factura",
			Monto: 15400.00, NumeroFactura: "FC-3", Confianza: 0.95,
		},
	}, downloads: map[string]string{}}
	dr := fakeDrive{files: map[string][]drive.FileEntry{}, downloads: map[string]string{}}

	plan, err := planBatch(context.Background(), []string{"/local/f3.pdf"}, cfg, cl, dr, "2026")
	if err != nil {
		t.Fatalf("planBatch returned error: %v", err)
	}
	if len(plan.NeedsReview) != 1 {
		t.Fatalf("expected 1 file needing review (unknown account number), got %d", len(plan.NeedsReview))
	}
}

func TestPlanBatch_PagoExactMatch(t *testing.T) {
	cfg := testHouses(t)
	// Shared between cl and dr: fakeDrive.Download records destLocalPath ->
	// fileID here; fakeClassifier.Classify reads it back to resolve the temp
	// path planBatch downloads the pending factura to.
	downloads := map[string]string{}

	cl := fakeClassifier{
		results: map[string]classify.Result{
			"/local/pago.jpeg": {
				NumeroCuenta: "AGUA-IGA-1", Categoria: "agua", Tipo: "pago",
				Monto: 15400.00, Confianza: 0.95,
			},
		},
		byFileID: map[string]classify.Result{
			// Re-classification of the pending factura already sitting in
			// Drive, downloaded by planBatch to learn its Monto.
			"drive-file-1": {
				NumeroCuenta: "AGUA-IGA-1", Categoria: "agua", Tipo: "factura",
				Monto: 15400.00, NumeroFactura: "FC-1", Confianza: 0.95,
			},
		},
		downloads: downloads,
	}
	dr := fakeDrive{
		files: map[string][]drive.FileEntry{
			"2026/Igarzabal/agua": {{Name: "01-FC-1.pdf", ID: "drive-file-1"}},
		},
		downloads: downloads,
	}

	plan, err := planBatch(context.Background(), []string{"/local/pago.jpeg"}, cfg, cl, dr, "2026")
	if err != nil {
		t.Fatalf("planBatch returned error: %v", err)
	}
	if len(plan.Uploads) != 1 {
		t.Fatalf("expected 1 planned upload, got %d (NeedsReview=%v, Disambiguate=%v)", len(plan.Uploads), plan.NeedsReview, plan.Disambiguate)
	}
	got := plan.Uploads[0]
	if got.TargetFilename != "01-FC-1 pago.jpeg" {
		t.Fatalf("expected filename '01-FC-1 pago.jpeg', got %q", got.TargetFilename)
	}
	if got.TargetPath != "2026/Igarzabal/agua" {
		t.Fatalf("expected path 2026/Igarzabal/agua, got %q", got.TargetPath)
	}
}
```

- [ ] **Step 2: Run tests to verify the first three fail, design and implement the pago-matching data flow, then implement `planBatch`**

```go
// main.go (partial — orchestration core; CLI entrypoint in Step 4)
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nicob201/billsort/internal/classify"
	"github.com/nicob201/billsort/internal/config"
	"github.com/nicob201/billsort/internal/drive"
	"github.com/nicob201/billsort/internal/match"
)

type PlannedUpload struct {
	LocalPath      string
	TargetPath     string // e.g. "2026/Igarzabal/agua"
	TargetFilename string
}

type NeedsReview struct {
	LocalPath string
	Reason    string
}

type NeedsDisambiguation struct {
	LocalPath  string
	Candidates []match.PendingFactura
}

type BatchPlan struct {
	Uploads       []PlannedUpload
	NeedsReview   []NeedsReview
	Disambiguate  []NeedsDisambiguation
	StillUnpaid   []string // human-readable summary lines for the final report
}

func planBatch(ctx context.Context, files []string, cfg *config.Houses, cl classify.Classifier, dr drive.Client, year string) (BatchPlan, error) {
	var plan BatchPlan

	for _, f := range files {
		result, err := cl.Classify(ctx, f)
		if err != nil {
			return BatchPlan{}, fmt.Errorf("classifying %q: %w", f, err)
		}
		if !result.IsUsable() {
			plan.NeedsReview = append(plan.NeedsReview, NeedsReview{LocalPath: f, Reason: "baja confianza o datos incompletos"})
			continue
		}

		casa, ok := cfg.FindCasa(result.Categoria, result.NumeroCuenta)
		if !ok {
			plan.NeedsReview = append(plan.NeedsReview, NeedsReview{LocalPath: f, Reason: fmt.Sprintf("numero_cuenta %q no reconocido para categoria %q", result.NumeroCuenta, result.Categoria)})
			continue
		}

		targetPath := fmt.Sprintf("%s/%s/%s", year, casa, result.Categoria)

		if result.Tipo == "factura" {
			existing, err := dr.ListFiles(ctx, []string{year, casa, result.Categoria})
			if err != nil {
				return BatchPlan{}, fmt.Errorf("listing %q: %w", targetPath, err)
			}
			nns := make([]int, 0, len(existing))
			for _, entry := range existing {
				if nn, ok := drive.ParseNN(entry.Name); ok {
					nns = append(nns, nn)
				}
			}
			nn := match.NextNN(nns)
			ext := extOf(f)
			filename := match.BuildFilename(nn, result.NumeroFactura, "factura", ext)
			plan.Uploads = append(plan.Uploads, PlannedUpload{LocalPath: f, TargetPath: targetPath, TargetFilename: filename})
			continue
		}

		// result.Tipo == "pago": find pending (unpaired) facturas in this
		// folder and match by amount. See pendingFacturasFor below.
		pending, err := pendingFacturasFor(ctx, dr, cl, year, casa, result.Categoria)
		if err != nil {
			return BatchPlan{}, fmt.Errorf("resolving pending facturas for %q: %w", targetPath, err)
		}
		outcome := match.FindPagoMatch(pending, result.Monto)
		switch outcome.Status {
		case match.MatchExact:
			ext := extOf(f)
			filename := match.BuildFilename(outcome.Match.NN, outcome.Match.NumeroFactura, "pago", ext)
			plan.Uploads = append(plan.Uploads, PlannedUpload{LocalPath: f, TargetPath: targetPath, TargetFilename: filename})
		case match.MatchAmbiguous:
			plan.Disambiguate = append(plan.Disambiguate, NeedsDisambiguation{LocalPath: f, Candidates: outcome.Candidates})
		case match.MatchNone:
			plan.NeedsReview = append(plan.NeedsReview, NeedsReview{LocalPath: f, Reason: fmt.Sprintf("no se encontro factura pendiente por %.2f en %s", result.Monto, targetPath)})
		}
	}

	return plan, nil
}

// pendingFacturasFor finds facturas already in the target Drive folder that
// don't yet have a matching " pago" file. Drive only stores filenames, not
// amounts, so each pending factura is downloaded and re-classified to learn
// its Monto (needed by match.FindPagoMatch).
func pendingFacturasFor(ctx context.Context, dr drive.Client, cl classify.Classifier, year, casa, categoria string) ([]match.PendingFactura, error) {
	entries, err := dr.ListFiles(ctx, []string{year, casa, categoria})
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s/%s: %w", year, casa, categoria, err)
	}

	paidNNs := map[int]bool{}
	facturaEntries := map[int]drive.FileEntry{}
	for _, entry := range entries {
		nn, ok := drive.ParseNN(entry.Name)
		if !ok {
			continue
		}
		if isPagoFilename(entry.Name) {
			paidNNs[nn] = true
		} else {
			facturaEntries[nn] = entry
		}
	}

	var pending []match.PendingFactura
	for nn, entry := range facturaEntries {
		if paidNNs[nn] {
			continue
		}

		tmp, err := os.CreateTemp("", "billsort-pending-*")
		if err != nil {
			return nil, fmt.Errorf("creating temp file for %q: %w", entry.Name, err)
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)

		if err := dr.Download(ctx, entry.ID, tmpPath); err != nil {
			return nil, fmt.Errorf("downloading %q: %w", entry.Name, err)
		}
		result, err := cl.Classify(ctx, tmpPath)
		if err != nil {
			return nil, fmt.Errorf("re-classifying %q: %w", entry.Name, err)
		}
		pending = append(pending, match.PendingFactura{NN: nn, NumeroFactura: result.NumeroFactura, Monto: result.Monto})
	}
	return pending, nil
}

// isPagoFilename reports whether a billsort-convention filename is the
// payment-receipt half of a pair (base name ends in " pago" before the
// extension).
func isPagoFilename(name string) bool {
	ext := extOf(name)
	base := strings.TrimSuffix(name, "."+ext)
	return strings.HasSuffix(base, " pago")
}

func extOf(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			return path[i+1:]
		}
	}
	return ""
}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS, all tests across `config`, `match`, `drive`, `classify`, and the root package.

- [ ] **Step 4: Implement the CLI entrypoint (`func main()`)**

```go
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "uso: billsort <carpeta-local>")
		os.Exit(1)
	}
	folder := os.Args[1]

	ctx := context.Background()

	cfg, err := config.Load("houses.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error cargando houses.json: %v\n", err)
		os.Exit(1)
	}

	cl, err := classify.NewClient(ctx, os.Getenv("GEMINI_API_KEY"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error inicializando Gemini: %v\n", err)
		os.Exit(1)
	}

	dr, err := drive.NewClient(ctx, "credentials.json", "token.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error inicializando Drive: %v\n", err)
		os.Exit(1)
	}

	files, err := listLocalFiles(folder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error leyendo %q: %v\n", folder, err)
		os.Exit(1)
	}

	year := fmt.Sprintf("%d", time.Now().Year())
	plan, err := planBatch(ctx, files, cfg, cl, dr, year)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	printPlanSummary(plan)

	if len(plan.Disambiguate) > 0 {
		if err := resolveDisambiguation(&plan); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	if len(plan.Uploads) == 0 {
		fmt.Println("Nada para subir.")
		return
	}

	if !confirm("¿Confirmar subida? [y/N] ") {
		fmt.Println("Cancelado.")
		return
	}

	if err := executeUploads(ctx, dr, plan.Uploads, year); err != nil {
		fmt.Fprintf(os.Stderr, "error subiendo: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Listo.")
}
```

Implement the small remaining helpers referenced above (`listLocalFiles`, `printPlanSummary`, `resolveDisambiguation`, `confirm`, `executeUploads`) as straightforward, self-contained functions — no new external dependencies, stdlib `bufio`/`os`/`fmt` only. `executeUploads` must call `dr.EnsureFolder` for each unique target path before `dr.Upload`, and must check for a name collision (via `dr.ListFiles`) before uploading, skipping and warning rather than overwriting, per spec.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: wire up CLI orchestration and entrypoint"
```

---

## Task 6: README — setup instructions

**Files:**
- Create: `README.md`

**Interfaces:** none (documentation only)

- [ ] **Step 1: Write `README.md`**

Cover, concretely (no placeholders):
1. What the tool does, one paragraph, in plain terms.
2. Prerequisites: Go installed, a Google Cloud project with the Drive API enabled and an OAuth "Desktop app" credential downloaded as `credentials.json`, a Gemini API key from Google AI Studio exported as `GEMINI_API_KEY`.
3. `cp houses.example.json houses.json` and fill in the 11 real account numbers.
4. `go build -o billsort .`
5. First run triggers the OAuth consent flow in the browser (caches to `token.json`).
6. Usage: `./billsort <carpeta-local>` — dry-run summary, single confirmation, then upload.
7. A short "Out of scope" note mirroring the spec: Rentas and Fiat Toro are not handled by this tool yet.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add setup and usage instructions"
```

---

## Self-Review Notes

**Spec coverage:** houses/categories (Task 1), NN + filename convention (Task 2), Drive folder structure/auto-create-year (Task 3), Gemini extraction + confidence gating (Task 4), dry-run summary + single confirmation + matching + interactive disambiguation + name-collision guard (Task 5), secrets never committed (Task 1 `.gitignore`, enforced throughout), setup docs (Task 6). No spec section without a task.

**Deviation flagged inline:** JSON instead of YAML for config (Global Constraints) — functionally equivalent, smaller dependency footprint, does not change any spec-level behavior or file layout the user interacts with beyond the extension.

**Resolved during planning:** `pendingFacturasFor` needs each pending factura's `Monto`, but Drive only stores filenames. Resolved by adding `drive.Client.Download` (Task 3) and `FileEntry.ID`, so Task 5 re-downloads and re-classifies any unpaired factura it finds before matching — fully specified in Task 5 Step 2, not left as an open decision.
