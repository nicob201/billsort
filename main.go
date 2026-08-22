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
	Uploads      []PlannedUpload
	NeedsReview  []NeedsReview
	Disambiguate []NeedsDisambiguation
	StillUnpaid  []string // human-readable summary lines for the final report
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
