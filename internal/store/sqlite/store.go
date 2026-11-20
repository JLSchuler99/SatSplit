package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"satsplit/internal/store"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// buildDSN appends pragmas that must be set on every pooled connection:
//   - foreign_keys: enforces ON DELETE CASCADE (off by default in SQLite).
//   - journal_mode=WAL: lets readers and a writer proceed concurrently
//     instead of blocking on the rollback journal.
//   - busy_timeout: makes contended writes retry for 5s instead of
//     immediately returning SQLITE_BUSY.
func buildDSN(path string) string {
	pragmas := []string{
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	}
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if strings.Contains(path, "?") {
		return path + "&" + q.Encode()
	}
	return path + "?" + q.Encode()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS receipts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token TEXT NOT NULL UNIQUE,
			lightning_address TEXT NOT NULL,
			receipt_filename TEXT NOT NULL,
			image_path TEXT NOT NULL DEFAULT '',
			tip_basis_points INTEGER NOT NULL DEFAULT 0,
			tax_cents INTEGER NOT NULL DEFAULT 0,
			currency_code TEXT NOT NULL DEFAULT 'USD',
			ocr_text TEXT NOT NULL DEFAULT '',
			items_status TEXT NOT NULL DEFAULT 'ocr',
			created_at TIMESTAMP NOT NULL,
			upload_id TEXT NOT NULL DEFAULT '',
			read_issue TEXT NOT NULL DEFAULT '',
			balance_checked INTEGER NOT NULL DEFAULT 0,
			balance_diff_cents INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			receipt_id INTEGER NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
			label TEXT NOT NULL,
			amount_cents INTEGER NOT NULL,
			split_count INTEGER NOT NULL DEFAULT 1
		);`,
		`CREATE TABLE IF NOT EXISTS selections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			receipt_id INTEGER NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
			participant TEXT NOT NULL,
			total_cents INTEGER NOT NULL,
			invoice TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS selection_items (
			selection_id INTEGER NOT NULL REFERENCES selections(id) ON DELETE CASCADE,
			item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
			PRIMARY KEY (selection_id, item_id)
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := ensureReceiptColumn(db, "image_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "tip_basis_points", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "currency_code", "TEXT NOT NULL DEFAULT 'USD'"); err != nil {
		return err
	}
	if err := ensureItemColumn(db, "split_count", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := ensureItemColumn(db, "translated_label", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureItemColumn(db, "quantity", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	// Host tokens. A receipt created before they existed gets its one shared
	// token as both values, so links already handed out keep working with no
	// special case anywhere else.
	if err := ensureColumn(db, "receipts", "host_token", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE receipts SET host_token = token WHERE host_token = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS receipts_host_token ON receipts(host_token)`); err != nil {
		return err
	}
	for _, col := range [][2]string{
		{"verify_url", "TEXT NOT NULL DEFAULT ''"},
		{"amount_sats", "INTEGER NOT NULL DEFAULT 0"},
		{"preimage", "TEXT NOT NULL DEFAULT ''"},
		{"paid_at", "TIMESTAMP"},
		{"edit_secret", "TEXT NOT NULL DEFAULT ''"},
		{"parent_id", "INTEGER REFERENCES selections(id) ON DELETE CASCADE"},
	} {
		if err := ensureColumn(db, "selections", col[0], col[1]); err != nil {
			return err
		}
	}
	if err := ensureColumn(db, "selection_items", "quantity", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "items_status", "TEXT NOT NULL DEFAULT 'ocr'"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "tax_cents", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "upload_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "read_issue", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "balance_checked", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureReceiptColumn(db, "balance_diff_cents", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// Empty upload ids are the receipts that predate client upload ids, and
	// they must not collide with each other.
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS receipts_upload_id ON receipts(upload_id) WHERE upload_id != ''`); err != nil {
		return err
	}
	return nil
}

func (s *Store) CreateReceipt(receipt store.Receipt) (store.Receipt, error) {
	now := receipt.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if receipt.ItemsStatus == "" {
		receipt.ItemsStatus = store.ItemsOCR
	}
	if receipt.HostToken == "" {
		receipt.HostToken = receipt.Token
	}
	res, err := s.db.Exec(
		`INSERT INTO receipts (token, host_token, lightning_address, receipt_filename, image_path, tip_basis_points, currency_code, ocr_text, items_status, created_at, upload_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.Token,
		receipt.HostToken,
		receipt.LightningAddress,
		receipt.ReceiptFilename,
		receipt.ImagePath,
		receipt.TipBasisPoints,
		receipt.CurrencyCode,
		receipt.OCRText,
		receipt.ItemsStatus,
		now,
		receipt.UploadID,
	)
	if err != nil {
		if receipt.UploadID != "" && strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "upload_id") {
			return store.Receipt{}, store.ErrUploadExists
		}
		return store.Receipt{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return store.Receipt{}, err
	}
	receipt.ID = id
	receipt.CreatedAt = now
	return receipt, nil
}

func (s *Store) UpdateReceiptOCR(receiptID int64, ocrText string) error {
	_, err := s.db.Exec(`UPDATE receipts SET ocr_text = ? WHERE id = ?`, ocrText, receiptID)
	return err
}

func (s *Store) UpdateReceiptTip(receiptID int64, tipBasisPoints int64) error {
	_, err := s.db.Exec(`UPDATE receipts SET tip_basis_points = ? WHERE id = ?`, tipBasisPoints, receiptID)
	return err
}

func (s *Store) UpdateReceiptTax(receiptID int64, taxCents int64) error {
	_, err := s.db.Exec(`UPDATE receipts SET tax_cents = ? WHERE id = ?`, taxCents, receiptID)
	return err
}

func (s *Store) ReplaceReceiptItems(receiptID int64, items []store.Item) ([]store.Item, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`DELETE FROM items WHERE receipt_id = ?`, receiptID); err != nil {
		return nil, err
	}

	for i := range items {
		item := items[i]
		if item.Quantity <= 0 {
			item.Quantity = 1
		}
		items[i].Quantity = item.Quantity
		res, err := tx.Exec(
			`INSERT INTO items (receipt_id, label, translated_label, amount_cents, split_count, quantity) VALUES (?, ?, ?, ?, ?, ?)`,
			receiptID, item.Label, item.TranslatedLabel, item.AmountCents, item.SplitCount, item.Quantity,
		)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		items[i].ID = id
		items[i].ReceiptID = receiptID
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ReplaceAutoItems(receiptID int64, items []store.Item, status string) (bool, []store.Item, error) {
	if status == "" {
		status = store.ItemsRefined
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return false, nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	res, err := tx.Exec(
		`UPDATE receipts SET items_status = ?
		 WHERE id = ? AND items_status = ?
		   AND NOT EXISTS (SELECT 1 FROM selections WHERE receipt_id = ?)`,
		status, receiptID, store.ItemsOCR, receiptID,
	)
	if err != nil {
		return false, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil, err
	}
	if n == 0 {
		return false, nil, nil
	}

	if _, err := tx.Exec(`DELETE FROM items WHERE receipt_id = ?`, receiptID); err != nil {
		return false, nil, err
	}
	for i := range items {
		item := items[i]
		if item.SplitCount <= 0 {
			item.SplitCount = 1
		}
		if item.Quantity <= 0 {
			item.Quantity = 1
		}
		items[i].Quantity = item.Quantity
		res, err := tx.Exec(
			`INSERT INTO items (receipt_id, label, translated_label, amount_cents, split_count, quantity) VALUES (?, ?, ?, ?, ?, ?)`,
			receiptID, item.Label, item.TranslatedLabel, item.AmountCents, item.SplitCount, item.Quantity,
		)
		if err != nil {
			return false, nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return false, nil, err
		}
		items[i].ID = id
		items[i].ReceiptID = receiptID
		items[i].SplitCount = item.SplitCount
	}
	if err := tx.Commit(); err != nil {
		return false, nil, err
	}
	return true, items, nil
}

func (s *Store) MarkItemsManual(receiptID int64) error {
	_, err := s.db.Exec(
		`UPDATE receipts SET items_status = ?, read_issue = '', balance_checked = 0, balance_diff_cents = 0 WHERE id = ?`,
		store.ItemsManual, receiptID,
	)
	return err
}

func (s *Store) SetReadState(receiptID int64, issue string, balanceChecked bool, balanceDiffCents int64) error {
	checked := 0
	if balanceChecked {
		checked = 1
	}
	_, err := s.db.Exec(
		`UPDATE receipts SET read_issue = ?, balance_checked = ?, balance_diff_cents = ?
		 WHERE id = ? AND items_status = ?
		   AND NOT EXISTS (SELECT 1 FROM selections WHERE receipt_id = ?)`,
		issue, checked, balanceDiffCents, receiptID, store.ItemsOCR, receiptID,
	)
	return err
}

func (s *Store) UpdateItemsStatus(receiptID int64, status string) error {
	_, err := s.db.Exec(
		`UPDATE receipts SET items_status = ? WHERE id = ? AND items_status = ?`,
		status, receiptID, store.ItemsOCR,
	)
	return err
}

func (s *Store) UpdateItemTranslations(receiptID int64, items []store.Item) error {
	for _, item := range items {
		if item.ID == 0 || item.TranslatedLabel == "" {
			continue
		}
		_, err := s.db.Exec(
			`UPDATE items SET translated_label = ? WHERE id = ? AND receipt_id = ?`,
			item.TranslatedLabel, item.ID, receiptID,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AdjustItemSplitCount(receiptID int64, itemID int64, delta int64) error {
	res, err := s.db.Exec(
		`UPDATE items SET split_count = MAX(1, split_count + ?) WHERE id = ? AND receipt_id = ?`,
		delta, itemID, receiptID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("item not found")
	}
	return nil
}

const receiptColumns = `id, token, host_token, lightning_address, receipt_filename, image_path, tip_basis_points, tax_cents, currency_code, ocr_text, items_status, created_at, upload_id, read_issue, balance_checked, balance_diff_cents`

func scanReceipt(row *sql.Row) (store.Receipt, error) {
	var receipt store.Receipt
	var checked int
	err := row.Scan(
		&receipt.ID, &receipt.Token, &receipt.HostToken, &receipt.LightningAddress,
		&receipt.ReceiptFilename, &receipt.ImagePath, &receipt.TipBasisPoints, &receipt.TaxCents,
		&receipt.CurrencyCode, &receipt.OCRText, &receipt.ItemsStatus, &receipt.CreatedAt,
		&receipt.UploadID, &receipt.ReadIssue, &checked, &receipt.BalanceDiffCents,
	)
	if err != nil {
		return store.Receipt{}, err
	}
	receipt.BalanceChecked = checked != 0
	return receipt, nil
}

func (s *Store) loadReceipt(receipt store.Receipt) (store.ReceiptWithItems, error) {
	items, err := s.listItems(receipt.ID)
	if err != nil {
		return store.ReceiptWithItems{}, err
	}
	return store.ReceiptWithItems{Receipt: receipt, Items: items}, nil
}

func (s *Store) GetReceiptByToken(token string) (store.ReceiptWithItems, error) {
	receipt, err := scanReceipt(s.db.QueryRow(
		`SELECT `+receiptColumns+`
		 FROM receipts WHERE host_token = ?1 OR token = ?1
		 ORDER BY (host_token = ?1) DESC LIMIT 1`,
		token,
	))
	if err != nil {
		return store.ReceiptWithItems{}, err
	}
	return s.loadReceipt(receipt)
}

func (s *Store) GetReceiptByUploadID(uploadID string) (store.ReceiptWithItems, error) {
	if uploadID == "" {
		return store.ReceiptWithItems{}, sql.ErrNoRows
	}
	receipt, err := scanReceipt(s.db.QueryRow(
		`SELECT `+receiptColumns+` FROM receipts WHERE upload_id = ?`,
		uploadID,
	))
	if err != nil {
		return store.ReceiptWithItems{}, err
	}
	return s.loadReceipt(receipt)
}

func (s *Store) listItems(receiptID int64) ([]store.Item, error) {
	rows, err := s.db.Query(
		`SELECT id, receipt_id, label, translated_label, amount_cents, split_count, quantity FROM items WHERE receipt_id = ? ORDER BY id`,
		receiptID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []store.Item
	for rows.Next() {
		var item store.Item
		if err := rows.Scan(&item.ID, &item.ReceiptID, &item.Label, &item.TranslatedLabel, &item.AmountCents, &item.SplitCount, &item.Quantity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetItemsByIDs(receiptID int64, itemIDs []int64) ([]store.Item, error) {
	if len(itemIDs) == 0 {
		return []store.Item{}, nil
	}
	placeholders := make([]string, 0, len(itemIDs))
	args := make([]any, 0, len(itemIDs)+1)
	args = append(args, receiptID)
	for _, id := range itemIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := fmt.Sprintf(
		`SELECT id, receipt_id, label, translated_label, amount_cents, split_count, quantity FROM items
		 WHERE receipt_id = ? AND id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []store.Item
	for rows.Next() {
		var item store.Item
		if err := rows.Scan(&item.ID, &item.ReceiptID, &item.Label, &item.TranslatedLabel, &item.AmountCents, &item.SplitCount, &item.Quantity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateSelection(selection store.Selection, itemIDs []int64, quantities map[int64]int64, replaceID *int64) (store.Selection, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return store.Selection{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if replaceID != nil {
		// Verify the old selection belongs to the same receipt, then delete it.
		// CASCADE will clean up selection_items. Silent no-op if not found.
		var oldReceiptID int64
		err := tx.QueryRow(`SELECT receipt_id FROM selections WHERE id = ?`, *replaceID).Scan(&oldReceiptID)
		if err == nil && oldReceiptID == selection.ReceiptID {
			if _, err := tx.Exec(`DELETE FROM selections WHERE id = ?`, *replaceID); err != nil {
				return store.Selection{}, err
			}
		}
		// If err != nil (not found), silently continue — stale localStorage
	}

	now := selection.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := tx.Exec(
		`INSERT INTO selections (receipt_id, participant, total_cents, invoice, verify_url, amount_sats, created_at, edit_secret)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		selection.ReceiptID,
		selection.Participant,
		selection.TotalCents,
		selection.Invoice,
		selection.VerifyURL,
		selection.AmountSats,
		now,
		selection.EditSecret,
	)
	if err != nil {
		return store.Selection{}, err
	}
	selectionID, err := res.LastInsertId()
	if err != nil {
		return store.Selection{}, err
	}
	for _, itemID := range itemIDs {
		quantity := quantities[itemID]
		if quantity <= 0 {
			quantity = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO selection_items (selection_id, item_id, quantity) VALUES (?, ?, ?)`,
			selectionID, itemID, quantity,
		); err != nil {
			return store.Selection{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return store.Selection{}, err
	}
	selection.ID = selectionID
	selection.CreatedAt = now
	return selection, nil
}

func (s *Store) AddToPaidSelection(rootID int64, itemIDs []int64, quantities map[int64]int64, addOn store.Selection) (store.Selection, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return store.Selection{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var receiptID int64
	if err := tx.QueryRow(`SELECT receipt_id FROM selections WHERE id = ? AND parent_id IS NULL`, rootID).Scan(&receiptID); err != nil {
		return store.Selection{}, err
	}
	if receiptID != addOn.ReceiptID {
		return store.Selection{}, errors.New("selection belongs to another receipt")
	}
	// A second invoice that was never paid is superseded by this one.
	if _, err := tx.Exec(`DELETE FROM selections WHERE parent_id = ? AND paid_at IS NULL`, rootID); err != nil {
		return store.Selection{}, err
	}
	if _, err := tx.Exec(`DELETE FROM selection_items WHERE selection_id = ?`, rootID); err != nil {
		return store.Selection{}, err
	}
	for _, itemID := range itemIDs {
		quantity := quantities[itemID]
		if quantity <= 0 {
			quantity = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO selection_items (selection_id, item_id, quantity) VALUES (?, ?, ?)`,
			rootID, itemID, quantity,
		); err != nil {
			return store.Selection{}, err
		}
	}

	now := addOn.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := tx.Exec(
		`INSERT INTO selections (receipt_id, participant, total_cents, invoice, verify_url, amount_sats, created_at, parent_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		addOn.ReceiptID, addOn.Participant, addOn.TotalCents, addOn.Invoice, addOn.VerifyURL, addOn.AmountSats, now, rootID,
	)
	if err != nil {
		return store.Selection{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return store.Selection{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Selection{}, err
	}
	addOn.ID = id
	addOn.CreatedAt = now
	addOn.ParentID = &rootID
	return addOn, nil
}

func (s *Store) MarkSelectionPaid(selectionID int64, preimage string, paidAt time.Time) error {
	_, err := s.db.Exec(
		`UPDATE selections SET paid_at = ?, preimage = ? WHERE id = ? AND paid_at IS NULL`,
		paidAt.UTC(), preimage, selectionID,
	)
	return err
}

func (s *Store) ListSelections(receiptID int64) ([]store.SelectionSummary, error) {
	rows, err := s.db.Query(
		`SELECT id, participant, total_cents, invoice, created_at, verify_url, amount_sats, preimage, paid_at, parent_id, edit_secret
		 FROM selections WHERE receipt_id = ? ORDER BY created_at`,
		receiptID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []store.SelectionSummary
	for rows.Next() {
		var ss store.SelectionSummary
		var paidAt sql.NullTime
		var parentID sql.NullInt64
		if err := rows.Scan(&ss.ID, &ss.Participant, &ss.TotalCents, &ss.Invoice, &ss.CreatedAt,
			&ss.VerifyURL, &ss.AmountSats, &ss.Preimage, &paidAt, &parentID, &ss.EditSecret); err != nil {
			return nil, err
		}
		if parentID.Valid {
			id := parentID.Int64
			ss.ParentID = &id
		}
		ss.Verifiable = ss.VerifyURL != ""
		if paidAt.Valid {
			at := paidAt.Time
			ss.Paid = true
			ss.PaidAt = &at
		}
		summaries = append(summaries, ss)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range summaries {
		itemRows, err := s.db.Query(
			`SELECT i.id, i.label, si.quantity FROM items i
			 JOIN selection_items si ON si.item_id = i.id
			 WHERE si.selection_id = ? ORDER BY i.id`,
			summaries[i].ID,
		)
		if err != nil {
			return nil, err
		}
		var refs []store.SelectionItemRef
		for itemRows.Next() {
			var ref store.SelectionItemRef
			if err := itemRows.Scan(&ref.ID, &ref.Label, &ref.Quantity); err != nil {
				itemRows.Close()
				return nil, err
			}
			refs = append(refs, ref)
		}
		itemRows.Close()
		if err := itemRows.Err(); err != nil {
			return nil, err
		}
		summaries[i].Items = refs
	}

	return summaries, nil
}

func ensureReceiptColumn(db *sql.DB, name, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(receipts)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if colName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec(`ALTER TABLE receipts ADD COLUMN ` + name + ` ` + definition)
	return err
}

func ensureItemColumn(db *sql.DB, name, definition string) error {
	return ensureColumn(db, "items", name, definition)
}

// ensureColumn adds a column when it is missing. table and name are fixed
// strings from this package, never user input.
func ensureColumn(db *sql.DB, table, name, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if colName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition)
	return err
}
