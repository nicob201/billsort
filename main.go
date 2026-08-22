// main.go (partial — orchestration core; CLI entrypoint in Step 5)
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nicob201/billsort/internal/classify"
	"github.com/nicob201/billsort/internal/config"
	"github.com/nicob201/billsort/internal/drive"
	"github.com/nicob201/billsort/internal/match"
)

// stdin is shared by confirm and resolveDisambiguation so buffered-but-not-
// yet-consumed input from one prompt isn't lost before the next prompt reads.
var stdin = bufio.NewReader(os.Stdin)

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
	TargetPath string // e.g. "2026/Igarzabal/agua" — needed to build the PlannedUpload after the user picks a candidate
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
			plan.Disambiguate = append(plan.Disambiguate, NeedsDisambiguation{LocalPath: f, TargetPath: targetPath, Candidates: outcome.Candidates})
		case match.MatchNone:
			plan.NeedsReview = append(plan.NeedsReview, NeedsReview{LocalPath: f, Reason: fmt.Sprintf("no se encontro factura pendiente por %.2f en %s", result.Monto, targetPath)})
		}
	}

	unpaid, err := stillUnpaidReport(ctx, dr, cfg, year)
	if err != nil {
		return BatchPlan{}, fmt.Errorf("checking for still-unpaid facturas: %w", err)
	}
	plan.StillUnpaid = unpaid

	return plan, nil
}

// stillUnpaidReport scans every configured house/category (not just the ones
// touched by files in this batch) for facturas that don't have a paired
// "pago" file yet, and returns human-readable summary lines for the final
// report. Unlike pendingFacturasFor, this never downloads or re-classifies —
// it only needs to know a factura exists unpaired, not its amount, so it's
// cheap (no extra Gemini calls) even though it checks every house/category
// on every run.
func stillUnpaidReport(ctx context.Context, dr drive.Client, cfg *config.Houses, year string) ([]string, error) {
	var lines []string
	for _, casa := range cfg.Casas {
		for categoria := range casa.Categorias {
			entries, err := dr.ListFiles(ctx, []string{year, casa.Nombre, categoria})
			if err != nil {
				return nil, fmt.Errorf("listing %s/%s/%s: %w", year, casa.Nombre, categoria, err)
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
			for nn, entry := range facturaEntries {
				if paidNNs[nn] {
					continue
				}
				lines = append(lines, fmt.Sprintf("%s/%s: %s sin pago", casa.Nombre, categoria, entry.Name))
			}
		}
	}
	return lines, nil
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

// listLocalFiles returns the full paths of the classifiable documents (pdf,
// jpg, jpeg, png) directly inside folder. The read is flat — houses and
// categories are mixed together because classification determines routing.
// Hidden files and anything with an unsupported extension are skipped rather
// than failing the whole run.
func listLocalFiles(folder string) ([]string, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("leyendo %q: %w", folder, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".pdf", ".jpg", ".jpeg", ".png":
			out = append(out, filepath.Join(folder, e.Name()))
		default:
			fmt.Fprintf(os.Stderr, "aviso: se ignora %q (formato no soportado)\n", e.Name())
		}
	}
	return out, nil
}

// printPlanSummary prints the dry-run view of the batch plan before anything
// touches Drive or is confirmed: planned uploads, files needing manual
// review, ambiguous pagos awaiting the interactive prompt, and — as its own
// section — the "pendientes de pago" report across every configured
// house/category, which is the recurring pending-payments value the tool
// adds beyond just filing documents.
func printPlanSummary(plan BatchPlan) {
	if len(plan.Uploads) > 0 {
		fmt.Println("== Subidas planificadas ==")
		for _, up := range plan.Uploads {
			fmt.Printf("  %s → %s/%s\n", up.LocalPath, up.TargetPath, up.TargetFilename)
		}
	}
	if len(plan.Disambiguate) > 0 {
		fmt.Println("\n== Pagos con más de una factura candidata ==")
		for _, d := range plan.Disambiguate {
			fmt.Printf("  %s (%d candidatas, se te va a preguntar)\n", d.LocalPath, len(d.Candidates))
		}
	}
	if len(plan.NeedsReview) > 0 {
		fmt.Println("\n== Requieren revisión manual ==")
		for _, r := range plan.NeedsReview {
			fmt.Printf("  %s: %s\n", r.LocalPath, r.Reason)
		}
	}
	if len(plan.StillUnpaid) > 0 {
		fmt.Println("\n== Pendientes de pago ==")
		for _, line := range plan.StillUnpaid {
			fmt.Printf("  %s\n", line)
		}
	}
}

// resolveDisambiguation interactively resolves each pago whose amount matched
// more than one pending factura: it lists the candidates, prompts for the NN
// of the correct one (or 's' to skip that file), re-prompts on invalid input,
// and on a valid choice appends a PlannedUpload built exactly like planBatch
// builds an unambiguous MatchExact upload.
func resolveDisambiguation(plan *BatchPlan) error {
	for _, d := range plan.Disambiguate {
		fmt.Printf("\n%s coincide con varias facturas pendientes:\n", d.LocalPath)
		for _, c := range d.Candidates {
			fmt.Printf("  NN %d — %s ($%.2f)\n", c.NN, c.NumeroFactura, c.Monto)
		}

		for {
			fmt.Print("NN de la factura correcta, o 's' para saltear este archivo: ")
			line, err := stdin.ReadString('\n')
			if err != nil {
				return fmt.Errorf("leyendo respuesta: %w", err)
			}
			ans := strings.TrimSpace(line)
			if strings.EqualFold(ans, "s") {
				fmt.Println("Salteado.")
				break
			}

			nn, err := strconv.Atoi(ans)
			if err != nil {
				fmt.Printf("%q no es un número; probá de nuevo.\n", ans)
				continue
			}
			chosen := -1
			for i, c := range d.Candidates {
				if c.NN == nn {
					chosen = i
					break
				}
			}
			if chosen < 0 {
				fmt.Printf("%d no es un NN de la lista; probá de nuevo.\n", nn)
				continue
			}

			c := d.Candidates[chosen]
			ext := extOf(d.LocalPath)
			filename := match.BuildFilename(c.NN, c.NumeroFactura, "pago", ext)
			plan.Uploads = append(plan.Uploads, PlannedUpload{LocalPath: d.LocalPath, TargetPath: d.TargetPath, TargetFilename: filename})
			fmt.Printf("Elegido NN %d → se subirá como %s/%s\n", c.NN, d.TargetPath, filename)
			break
		}
	}
	return nil
}

// confirm prints prompt and reads a yes/no answer from stdin. Anything other
// than an explicit yes defaults to no — uploads only happen on confirmation.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "si", "sí":
		return true
	default:
		return false
	}
}

// executeUploads performs the planned uploads after the user confirmed:
// it ensures each unique target folder exists (creating missing segments,
// e.g. the /<año>/ folder when a new year starts), checks for a name
// collision in Drive before each upload — warning and skipping instead of
// ever overwriting — and uploads file by file, printing each success so a
// mid-batch failure leaves the user knowing exactly what got uploaded.
func executeUploads(ctx context.Context, dr drive.Client, uploads []PlannedUpload, year string) error {
	folderIDs := map[string]string{}         // TargetPath -> Drive folder ID
	existing := map[string]map[string]bool{} // TargetPath -> filenames already in Drive there
	uploaded := map[string]bool{}            // TargetPath/filename pairs uploaded during this run

	for _, up := range uploads {
		folderID, ok := folderIDs[up.TargetPath]
		if !ok {
			id, err := dr.EnsureFolder(ctx, strings.Split(up.TargetPath, "/"))
			if err != nil {
				return fmt.Errorf("asegurando carpeta %q: %w", up.TargetPath, err)
			}
			names := map[string]bool{}
			entries, err := dr.ListFiles(ctx, strings.Split(up.TargetPath, "/"))
			if err != nil {
				return fmt.Errorf("listando %q para chequear colisiones: %w", up.TargetPath, err)
			}
			for _, e := range entries {
				names[e.Name] = true
			}
			folderIDs[up.TargetPath] = id
			existing[up.TargetPath] = names
			folderID = id
		}

		key := up.TargetPath + "/" + up.TargetFilename
		if existing[up.TargetPath][up.TargetFilename] || uploaded[key] {
			fmt.Fprintf(os.Stderr, "aviso: %q ya existe en %q; se saltea para no sobrescribir\n", up.TargetFilename, up.TargetPath)
			continue
		}

		if err := dr.Upload(ctx, folderID, up.TargetFilename, up.LocalPath); err != nil {
			return fmt.Errorf("subiendo %q como %q en %q: %w", up.LocalPath, up.TargetFilename, up.TargetPath, err)
		}
		uploaded[key] = true
		existing[up.TargetPath][up.TargetFilename] = true
		fmt.Printf("subido: %s → %s/%s\n", up.LocalPath, up.TargetPath, up.TargetFilename)
	}
	return nil
}
