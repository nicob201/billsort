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

func TestPlanBatch_StillUnpaidAcrossAllCategories(t *testing.T) {
	cfg := &config.Houses{Casas: []config.Casa{
		{Nombre: "Igarzabal", Categorias: map[string]config.Categoria{
			"agua": {NumeroCuenta: "AGUA-IGA-1"},
			"luz":  {NumeroCuenta: "LUZ-IGA-1"},
		}},
	}}
	cl := fakeClassifier{results: map[string]classify.Result{}, downloads: map[string]string{}}
	dr := fakeDrive{
		files: map[string][]drive.FileEntry{
			// agua: one unpaired factura. luz: fully paid, nothing to report.
			"2026/Igarzabal/agua": {{Name: "01-FC-1.pdf", ID: "d1"}},
			"2026/Igarzabal/luz":  {{Name: "01-0007 - 1.pdf", ID: "d2"}, {Name: "01-0007 - 1 pago.pdf", ID: "d3"}},
		},
		downloads: map[string]string{},
	}

	// No local files this run — StillUnpaid must still be populated by
	// scanning cfg's houses/categories directly, not derived from the batch.
	plan, err := planBatch(context.Background(), nil, cfg, cl, dr, "2026")
	if err != nil {
		t.Fatalf("planBatch returned error: %v", err)
	}
	if len(plan.StillUnpaid) != 1 {
		t.Fatalf("expected 1 still-unpaid line, got %d: %v", len(plan.StillUnpaid), plan.StillUnpaid)
	}
	want := "Igarzabal/agua: 01-FC-1.pdf sin pago"
	if plan.StillUnpaid[0] != want {
		t.Fatalf("got %q, want %q", plan.StillUnpaid[0], want)
	}
}
