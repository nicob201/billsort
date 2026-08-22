// internal/classify/classify.go

// Package classify extracts structured data from one local document
// (factura or comprobante de pago) via the Gemini API, behind a narrow
// Classifier interface so billsort's orchestration logic can be tested with
// a fake without live API calls.
package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/genai"
)

// geminiModel is the Gemini model used for document extraction. It's a
// single constant so it can be swapped as new models ship.
const geminiModel = "gemini-2.5-flash"

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
type geminiClassifier struct {
	client *genai.Client
}

// NewClient builds a Classifier backed by the live Gemini API using apiKey.
func NewClient(ctx context.Context, apiKey string) (Classifier, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Gemini API key vacía (exportá GEMINI_API_KEY)")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("creando cliente de Gemini: %w", err)
	}
	return &geminiClassifier{client: client}, nil
}

// extractionPrompt tells Gemini exactly which JSON shape to return. Per the
// plan: categoria must be agua|gas|luz, tipo must be factura|pago, and the
// model must report low confianza rather than fabricate a value it can't
// confidently read — IsUsable turns that into a "needs manual review" skip.
const extractionPrompt = `Sos un extractor de datos de documentos fiscales argentinos (facturas y comprobantes de pago de servicios: agua, gas, luz).

Analizá el documento adjunto y respondé EXCLUSIVAMENTE con un objeto JSON válido, sin texto adicional, con exactamente estos campos:
{
  "numero_cuenta": string,   // número de cliente/cuenta fijo impreso en la factura (NO el número de factura)
  "categoria": string,       // uno de: "agua", "gas", "luz"
  "tipo": string,            // "factura" si es la factura; "pago" si es el comprobante de pago
  "monto": number,           // importe total a pagar, en pesos argentinos
  "numero_factura": string,  // número de factura impreso por la empresa; puede ser "" si no figura o no aplica
  "confianza": number        // tu confianza en esta extracción, entre 0.0 y 1.0
}

Reglas importantes:
- Si algún campo requerido no se puede leer con confianza del documento, NO inventes un valor: devolvé el campo vacío o 0 según corresponda y poné una confianza baja (por ejemplo menor a 0.5).
- El monto es el importe total del período, no pagos parciales ni recargos de otros conceptos.
- No agregues campos extra ni explicaciones.`

// Classify sends the document at localPath to Gemini as multimodal input
// (PDF via document input, JPG/JPEG/PNG via image input — media type
// inferred from the file extension), requests structured JSON output, and
// parses it into a Result.
func (c *geminiClassifier) Classify(ctx context.Context, localPath string) (Result, error) {
	data, mime, err := readDocument(localPath)
	if err != nil {
		return Result{}, err
	}

	// One Content holding both parts: the extraction prompt and the
	// attached document (PDF via document input, images via image input).
	contents := genai.Text(extractionPrompt)
	contents[0].Parts = append(contents[0].Parts, genai.NewPartFromBytes(data, mime))

	resp, err := c.client.Models.GenerateContent(ctx, geminiModel, contents,
		&genai.GenerateContentConfig{ResponseMIMEType: "application/json"},
	)
	if err != nil {
		return Result{}, fmt.Errorf("llamando a Gemini para %q: %w", localPath, err)
	}

	txt := resp.Text()
	if strings.TrimSpace(txt) == "" {
		return Result{}, fmt.Errorf("Gemini devolvió una respuesta vacía para %q", localPath)
	}

	var raw struct {
		NumeroCuenta  string  `json:"numero_cuenta"`
		Categoria     string  `json:"categoria"`
		Tipo          string  `json:"tipo"`
		Monto         float64 `json:"monto"`
		NumeroFactura string  `json:"numero_factura"`
		Confianza     float64 `json:"confianza"`
	}
	if err := json.Unmarshal([]byte(txt), &raw); err != nil {
		return Result{}, fmt.Errorf("parseando respuesta de Gemini para %q (%.200q): %w", localPath, txt, err)
	}
	return Result{
		NumeroCuenta:  raw.NumeroCuenta,
		Categoria:     raw.Categoria,
		Tipo:          raw.Tipo,
		Monto:         raw.Monto,
		NumeroFactura: raw.NumeroFactura,
		Confianza:     raw.Confianza,
	}, nil
}

// readDocument reads localPath and returns its bytes plus the MIME type
// inferred from the extension. Only the input formats billsort supports are
// accepted (PDF/JPG/JPEG/PNG — anything else is an error).
func readDocument(localPath string) ([]byte, string, error) {
	var mime string
	switch strings.ToLower(filepath.Ext(localPath)) {
	case ".pdf":
		mime = "application/pdf"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".png":
		mime = "image/png"
	default:
		return nil, "", fmt.Errorf("formato no soportado %q (solo pdf, jpg, jpeg, png)", filepath.Ext(localPath))
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return nil, "", fmt.Errorf("leyendo %q: %w", localPath, err)
	}
	return data, mime, nil
}
