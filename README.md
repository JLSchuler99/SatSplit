# SatSplit

Split a restaurant receipt and settle up over Lightning.

The person who paid the bill uploads a photo of the receipt and shares a link.
Everyone else opens the link, picks what they ordered, and gets a Lightning
invoice for their share, with tax, service charge and tip included. Payments go
straight to the payer's Lightning address; SatSplit never holds funds.

SatSplit is a single Go binary with an embedded vanilla-JS frontend and a
SQLite database.

> **Status:** early and lightly tested. Expect rough edges.

## How it works

1. **Upload.** The host enters a Lightning address, picks the receipt's
   currency, and takes or chooses a photo. iPhone HEIC photos are converted to
   JPEG.
2. **Read.** A vision model reads the line items, quantities, tax and totals,
   and gives each item an English name. The server checks that the items add
   up to the printed subtotal or total and asks the model again when they do
   not. Identical lines are merged into one row with a quantity. Without a
   vision model, Tesseract OCR reads the receipt.
3. **Review.** The host corrects items if needed, sets the tip, and shares the
   link or QR code.
4. **Claim.** Each guest selects items. A row with several units lets a guest
   say how many they had, and an item can be split between people. Tax and
   service are shared in proportion to what each person ordered.
5. **Pay.** The server converts the guest's total to sats at the current rate
   and requests an invoice from the host's Lightning address (LNURL-pay).

### Knowing who paid

- If the host's Lightning address provider supports
  [LUD-21](https://github.com/lnurl/luds/blob/luds/21.md) (LNURL-verify), the
  host view shows each invoice as paid or not yet paid, and a guest who adds
  items after paying gets a second invoice for the difference only.
- Otherwise the host view shows each invoice's payment hash and amount in
  sats, to match by hand against the wallet's payment list.

## Running it

```sh
cp configs/.env.example .env   # then edit
docker compose up --build
```

Open http://localhost:8080.

### Configuration

| Variable | Purpose |
|---|---|
| `SAT_SPLIT_BASE_URL` | Public address used in share links, for example `https://split.example.com`. Set this behind a reverse proxy. |
| `VISION_URL` | Base URL of an OpenAI-compatible API with image input (`/v1/chat/completions`). When set, the vision model is the only receipt reader. `LLAMA_URL` is accepted as an alias. |
| `VISION_API_KEY` | API key for that endpoint, if it needs one. |
| `VISION_MODEL` | Model name to request. |
| `VISION_FALLBACK_MODEL` | Optional second model, asked only when the first one's items do not add up to the printed total. |
| `VISION_OCR_HINT` | Set to `1` to pass Tesseract's text to the vision model as a draft. |
| `TRANSLATE_URL` | Optional translation service (see `translate/`) for item names the host types in. |
| `SAT_SPLIT_ADDR` | Listen address. Default `:8080`. |
| `SAT_SPLIT_DB` | SQLite database path. |
| `SAT_SPLIT_UPLOAD_DIR` | Where receipt photos are stored. |
| `TESSERACT_LANG` | Default Tesseract language when no vision model is set. |

The container image includes Tesseract and `heif-convert`. Keep the database
and upload directory on a persistent volume.

## Privacy

- Receipt photos are stored on the server, along with what the reader
  returned for each one, so that reading quality can be reviewed later.
- With a hosted vision model, each receipt photo is sent to that provider.
- Anyone with a receipt's share link can see its items, who claimed what, and
  the invoices created for it.

## Development

```sh
go run ./cmd/satsplit
go test ./...
go vet ./...
```

Layout:

- `cmd/satsplit/` — entrypoint and configuration
- `internal/httpapi/` — HTTP handlers and the embedded frontend (`static/`)
- `internal/vision/` — vision client, total check and model fallback
- `internal/ocr/` — Tesseract reader
- `internal/lightning/` — LNURL-pay, LNURL-verify and BOLT11 payment hash
- `internal/store/` — storage interface and SQLite implementation
- `translate/` — optional translation service

## Known limitations

- The host's Lightning address is required; BOLT12 is not supported yet.
- Unit and share limits are enforced in the browser, not on the server.
- With a provider that cannot confirm payments, changing a selection after
  paying creates a new invoice for the full amount.
- Paid status is only refreshed while the host page is open.

## License

MIT. See [LICENSE](LICENSE).
