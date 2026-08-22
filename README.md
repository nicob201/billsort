# billsort

billsort is a small command-line tool that automates a recurring chore: filing
utility and tax documents (facturas and their payment receipts) into an
existing Google Drive folder structure. You drop the documents you received
this month — PDFs or photos — into any local folder, run `billsort` on it, and
it sends each document to the Gemini API to extract the account number,
category (agua/gas/luz), document type, amount, and invoice number. From that
it works out the correct Drive destination (`/<año>/<casa>/<categoría>/`) and
the correct filename (`NN-<número de factura>[ pago].<ext>`, where `NN` is one
more than the highest counter already in that folder), shows you a dry-run
summary of everything it plans to do, asks one yes/no confirmation, and then
uploads. It also pairs each `pago` receipt with its pending `factura` by exact
amount (asking you when the match is ambiguous), and prints a final report of
facturas that still have no matching pago — your outstanding-payments list.

## Prerequisites

1. **Go** installed (any recent version; built and tested with 1.27).
2. **A Google Cloud project** with the **Google Drive API enabled**, plus an
   OAuth **"Desktop app"** credential created under *APIs & Services →
   Credentials*. Download it and save it as `credentials.json` in this
   directory. Only the minimal `drive.file` scope is requested — billsort can
   only see and touch files it created itself.
3. **A Gemini API key** from [Google AI Studio](https://aistudio.google.com/),
   exported as an environment variable:

   ```sh
   export GEMINI_API_KEY="tu-api-key"
   ```

## Configuration

Copy the template and fill in the real account numbers (11 in total: agua/gas/
luz for Igarzabal, Pardos y Morenos and Yaco Misque; agua/luz for La Granja):

```sh
cp houses.example.json houses.json
```

Each category needs the fixed client/account number printed on that house's
bills — that number is how billsort decides which house a document belongs to.
`houses.json` holds secrets and is gitignored; so are `credentials.json`,
`token.json` and `.env`. Never commit them.

## Build

```sh
go build -o billsort .
```

## First run

The first time billsort connects to Drive it opens your browser for a
one-time Google OAuth consent screen. After you approve access, the resulting
token is cached locally in `token.json` and every later run reuses it without
asking again.

## Usage

```sh
./billsort <carpeta-local>
```

Point it at a folder containing the documents you want to file (flat — no need
to sort by house or type; classification handles routing). Supported input
formats: PDF, JPG, JPEG, PNG.

What happens on a run:

1. Every document is classified via Gemini.
2. A dry-run summary is printed: planned uploads (local file → Drive
   destination), anything needing manual review, ambiguous pagos, and a
   "pendientes de pago" section listing facturas across all your houses that
   still have no matching receipt.
3. If a pago's amount matches more than one pending factura, you're prompted
   to pick the right one (or skip that file).
4. One single `y/N` confirmation before anything is uploaded. Answering
   anything but `y` cancels without touching Drive.
5. On confirm, missing folders (including the `/2026/`-style year folder) are
   created automatically and the files are uploaded under their computed
   names. A filename collision in Drive warns and skips — nothing is ever
   overwritten.

## Out of scope

Two document families are deliberately **not** handled by this tool yet:
**Rentas** (paid annually per house) and **familiar vehicle** vehicle documents.
Their files in Drive are left untouched; don't put them in the input folder.
