// Package mail ingests IMAP messages and archives their untouched source.
//
// Ingestion and extraction are deliberately separate. This package's only job
// is to get every message stored exactly as it arrived; understanding it is
// somebody else's problem. That separation is what lets extraction be re-run
// with a better model later without touching the network.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NandishwarSingh/badal/internal/objects"
	"github.com/NandishwarSingh/badal/internal/secretbox"
)

type Service struct {
	pool *pgxpool.Pool
	obj  *objects.Store
	box  *secretbox.Box
}

func New(pool *pgxpool.Pool, obj *objects.Store, box *secretbox.Box) *Service {
	return &Service{pool: pool, obj: obj, box: box}
}

type Account struct {
	ID           uuid.UUID  `json:"id"`
	Email        string     `json:"email"`
	Host         string     `json:"imap_host"`
	Port         int        `json:"imap_port"`
	Username     string     `json:"username"`
	LastUID      uint32     `json:"last_uid"`
	UIDValidity  *uint32    `json:"uid_validity"`
	BackfillDone bool       `json:"backfill_done"`
	Enabled      bool       `json:"enabled"`
	LastSyncAt   *time.Time `json:"last_sync_at"`
	LastError    string     `json:"last_error"`
}

// ---------------------------------------------------------------- accounts

// AddAccount stores credentials after proving they work. Saving an account
// that cannot log in just moves the failure somewhere less visible.
func (s *Service) AddAccount(ctx context.Context, email, host string, port int, username, password string) (Account, error) {
	if username == "" {
		username = email
	}
	if host == "" {
		host = guessHost(email)
	}
	if port == 0 {
		port = 993
	}
	c, err := dial(host, port, username, password)
	if err != nil {
		return Account{}, fmt.Errorf("login failed: %w", err)
	}
	_ = c.Logout().Wait()
	c.Close()

	sealed, err := s.box.Seal(password)
	if err != nil {
		return Account{}, err
	}
	var a Account
	err = s.pool.QueryRow(ctx,
		`INSERT INTO mail_accounts (email, imap_host, imap_port, username, password_enc)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (email) DO UPDATE
		   SET imap_host=EXCLUDED.imap_host, imap_port=EXCLUDED.imap_port,
		       username=EXCLUDED.username, password_enc=EXCLUDED.password_enc,
		       enabled=true, last_error=NULL
		 RETURNING id, email, imap_host, imap_port, username, last_uid,
		           uid_validity, backfill_done, enabled, last_sync_at,
		           coalesce(last_error,'')`,
		email, host, port, username, sealed,
	).Scan(&a.ID, &a.Email, &a.Host, &a.Port, &a.Username, &a.LastUID,
		&a.UIDValidity, &a.BackfillDone, &a.Enabled, &a.LastSyncAt, &a.LastError)
	return a, err
}

func guessHost(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "imap.gmail.com"
	}
	switch strings.ToLower(email[at+1:]) {
	case "gmail.com", "googlemail.com":
		return "imap.gmail.com"
	case "outlook.com", "hotmail.com", "live.com":
		return "outlook.office365.com"
	}
	// Anything on Google Workspace -- including vitstudent.ac.in -- uses the
	// same host as consumer Gmail.
	return "imap.gmail.com"
}

func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, email, imap_host, imap_port, username, last_uid, uid_validity,
		        backfill_done, enabled, last_sync_at, coalesce(last_error,'')
		   FROM mail_accounts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.Host, &a.Port, &a.Username, &a.LastUID,
			&a.UIDValidity, &a.BackfillDone, &a.Enabled, &a.LastSyncAt, &a.LastError); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) DeleteAccount(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM mail_accounts WHERE id=$1`, id)
	return err
}

// ------------------------------------------------------------------- imap

func dial(host string, port int, user, pass string) (*imapclient.Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	c, err := imapclient.DialTLS(addr, &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		// University mail is full of non-UTF8 encoded-word headers; without a
		// charset reader those subjects arrive as mojibake.
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	})
	if err != nil {
		return nil, err
	}
	if err := c.Login(user, pass).Wait(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// SyncResult reports what one pass did.
type SyncResult struct {
	Fetched  int    `json:"fetched"`
	Skipped  int    `json:"skipped"`
	Backfill bool   `json:"backfill"`
	Error    string `json:"error,omitempty"`
}

// Sync pulls new mail for one account.
//
// First run backfills the configured window (7 days by default) so the
// calendar is useful immediately rather than only for mail that happens to
// arrive after setup. Later runs fetch strictly by UID.
func (s *Service) Sync(ctx context.Context, acc Account, backfillDays int) (SyncResult, error) {
	var res SyncResult
	var sealed []byte
	if err := s.pool.QueryRow(ctx,
		`SELECT password_enc FROM mail_accounts WHERE id=$1`, acc.ID).Scan(&sealed); err != nil {
		return res, err
	}
	pass, err := s.box.Open(sealed)
	if err != nil {
		return res, err
	}

	c, err := dial(acc.Host, acc.Port, acc.Username, pass)
	if err != nil {
		s.noteError(ctx, acc.ID, err)
		return res, err
	}
	defer func() { _ = c.Logout().Wait(); c.Close() }()

	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		s.noteError(ctx, acc.ID, err)
		return res, err
	}

	// A changed UIDVALIDITY means every UID we stored refers to something else
	// now. Treat it as a fresh mailbox rather than silently skipping mail.
	if acc.UIDValidity != nil && *acc.UIDValidity != sel.UIDValidity {
		slog.Warn("uidvalidity changed; resyncing", "account", acc.Email,
			"was", *acc.UIDValidity, "now", sel.UIDValidity)
		acc.LastUID = 0
		acc.BackfillDone = false
	}

	var uids []imap.UID
	if !acc.BackfillDone {
		res.Backfill = true
		since := time.Now().AddDate(0, 0, -backfillDays)
		crit := &imap.SearchCriteria{Since: since}
		data, err := c.UIDSearch(crit, nil).Wait()
		if err != nil {
			s.noteError(ctx, acc.ID, err)
			return res, err
		}
		uids = data.AllUIDs()
	} else {
		if sel.UIDNext > 0 && uint32(sel.UIDNext) <= acc.LastUID+1 {
			// Nothing new; avoid an empty FETCH round trip.
			s.noteOK(ctx, acc.ID, acc.LastUID, sel.UIDValidity, acc.BackfillDone)
			return res, nil
		}
		set := imap.UIDSetNum()
		set.AddRange(imap.UID(acc.LastUID+1), 0) // 0 = "*", through the end
		data, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
		if err != nil {
			s.noteError(ctx, acc.ID, err)
			return res, err
		}
		uids = data.AllUIDs()
	}

	maxUID := acc.LastUID
	for _, u := range uids {
		if uint32(u) <= acc.LastUID && acc.BackfillDone {
			continue
		}
		stored, err := s.fetchOne(ctx, c, acc, u)
		if err != nil {
			slog.Warn("fetch message", "uid", u, "err", err)
			continue
		}
		if stored {
			res.Fetched++
		} else {
			res.Skipped++
		}
		if uint32(u) > maxUID {
			maxUID = uint32(u)
		}
	}

	s.noteOK(ctx, acc.ID, maxUID, sel.UIDValidity, true)
	return res, nil
}

// fetchOne downloads a single message and archives it. Returns false when the
// message was already stored.
func (s *Service) fetchOne(ctx context.Context, c *imapclient.Client, acc Account, uid imap.UID) (bool, error) {
	set := imap.UIDSetNum(uid)
	fetchOpts := &imap.FetchOptions{
		Envelope:     true,
		InternalDate: true,
		RFC822Size:   true,
		BodySection:  []*imap.FetchItemBodySection{{}}, // whole raw message
	}
	msgs, err := c.Fetch(set, fetchOpts).Collect()
	if err != nil {
		return false, err
	}
	if len(msgs) == 0 {
		return false, nil
	}
	m := msgs[0]

	msgID := ""
	if m.Envelope != nil {
		msgID = strings.Trim(m.Envelope.MessageID, "<>")
	}
	if msgID == "" {
		// Some senders omit Message-ID. UID is unique within the mailbox and
		// stable as long as UIDVALIDITY holds, so it is a usable fallback.
		msgID = fmt.Sprintf("uid-%d@%s", uid, acc.Email)
	}

	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT exists(SELECT 1 FROM messages WHERE account_id=$1 AND message_id=$2)`,
		acc.ID, msgID).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	// FindBodySection matches the section we asked for rather than assuming
	// the server returned exactly one.
	raw := m.FindBodySection(&imap.FetchItemBodySection{})
	if raw == nil {
		for _, v := range m.BodySection {
			raw = v.Bytes
			break
		}
	}
	if len(raw) == 0 {
		return false, fmt.Errorf("empty body for uid %d", uid)
	}

	parsed := parseMIME(raw)

	received := m.InternalDate
	if m.Envelope != nil && !m.Envelope.Date.IsZero() {
		received = m.Envelope.Date
	}
	if received.IsZero() {
		received = time.Now()
	}

	subject, fromName, fromAddr := "", "", ""
	var to []string
	if m.Envelope != nil {
		subject = m.Envelope.Subject
		if len(m.Envelope.From) > 0 {
			fromName = m.Envelope.From[0].Name
			fromAddr = m.Envelope.From[0].Addr()
		}
		for _, a := range m.Envelope.To {
			to = append(to, a.Addr())
		}
	}
	if subject == "" {
		subject = parsed.Subject
	}

	// Archive the untouched source before recording the row: a message row
	// pointing at a missing object is worse than no row at all.
	key := fmt.Sprintf("mail/%s/%d-%s.eml", acc.ID, received.Unix(), shortHash(msgID))
	if err := s.obj.Put(ctx, key, raw); err != nil {
		return false, fmt.Errorf("archive: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO messages
		   (account_id, message_id, uid, subject, from_name, from_addr, to_addrs,
		    received_at, snippet, body_text, raw_key, raw_size, attachments)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		 ON CONFLICT (account_id, message_id) DO NOTHING`,
		acc.ID, msgID, uint32(uid), subject, fromName, fromAddr, to,
		received, snippet(parsed.Text), parsed.Text, key, len(raw), parsed.AttachmentsJSON())
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) noteOK(ctx context.Context, id uuid.UUID, lastUID, validity uint32, backfilled bool) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE mail_accounts
		    SET last_uid=$2, uid_validity=$3, backfill_done=$4,
		        last_sync_at=now(), last_error=NULL
		  WHERE id=$1`, id, lastUID, validity, backfilled)
}

func (s *Service) noteError(ctx context.Context, id uuid.UUID, err error) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE mail_accounts SET last_error=$2, last_sync_at=now() WHERE id=$1`,
		id, err.Error())
}

// SyncAll runs one pass over every enabled account.
func (s *Service) SyncAll(ctx context.Context, backfillDays int) map[string]SyncResult {
	out := map[string]SyncResult{}
	accs, err := s.Accounts(ctx)
	if err != nil {
		return out
	}
	for _, a := range accs {
		if !a.Enabled {
			continue
		}
		r, err := s.Sync(ctx, a, backfillDays)
		if err != nil {
			r.Error = err.Error()
		}
		out[a.Email] = r
	}
	return out
}

// Poll runs SyncAll forever. Polling rather than IMAP IDLE: IDLE needs a held
// connection with its own reconnect and keepalive logic, and a two minute
// delay on university mail is not worth that failure surface.
func (s *Service) Poll(ctx context.Context, every time.Duration, backfillDays int, after func(map[string]SyncResult)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		res := s.SyncAll(ctx, backfillDays)
		if after != nil {
			after(res)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------- reading

type Message struct {
	ID          uuid.UUID `json:"id"`
	MessageID   string    `json:"message_id"`
	Subject     string    `json:"subject"`
	FromName    string    `json:"from_name"`
	FromAddr    string    `json:"from_addr"`
	ReceivedAt  time.Time `json:"received_at"`
	Snippet     string    `json:"snippet"`
	BodyText    string    `json:"body_text,omitempty"`
	Extracted   bool      `json:"extracted"`
	Attachments []byte    `json:"-"`
}

// Pending returns messages that have not been through the extractor at the
// current version.
func (s *Service) Pending(ctx context.Context, version, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, message_id, subject, from_name, from_addr, received_at,
		        snippet, body_text
		   FROM messages
		  WHERE extracted_at IS NULL OR coalesce(extraction_version,0) < $1
		  ORDER BY received_at DESC LIMIT $2`, version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.MessageID, &m.Subject, &m.FromName, &m.FromAddr,
			&m.ReceivedAt, &m.Snippet, &m.BodyText); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) MarkExtracted(ctx context.Context, id uuid.UUID, version int, extractErr string) error {
	var ep *string
	if extractErr != "" {
		ep = &extractErr
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE messages SET extracted_at=now(), extraction_version=$2, extract_error=$3
		  WHERE id=$1`, id, version, ep)
	return err
}

func (s *Service) List(ctx context.Context, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, message_id, subject, from_name, from_addr, received_at, snippet,
		        (extracted_at IS NOT NULL)
		   FROM messages ORDER BY received_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.MessageID, &m.Subject, &m.FromName, &m.FromAddr,
			&m.ReceivedAt, &m.Snippet, &m.Extracted); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Raw returns the archived source, for re-parsing or debugging.
func (s *Service) Raw(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var key string
	err := s.pool.QueryRow(ctx, `SELECT raw_key FROM messages WHERE id=$1`, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		return nil, err
	}
	return s.obj.Get(ctx, key)
}

func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		return s[:220]
	}
	return s
}
