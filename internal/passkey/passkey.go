// Package passkey implements WebAuthn registration and login for the PWA.
//
// Why this exists: the CLI can safely hold a bearer token in a 0600 file, but a
// browser cannot. Anything in localStorage is readable by any XSS, and a stolen
// device token stays valid indefinitely. A passkey never leaves the
// authenticator, so there is nothing for a script to steal.
package passkey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionCookie = "badal_session"
	sessionTTL    = 90 * 24 * time.Hour
	ceremonyTTL   = 5 * time.Minute
)

var ErrUnauthorized = errors.New("unauthorized")

// owner is the single WebAuthn user. badal is deliberately single-tenant, so
// the handle is a fixed value rather than a per-account row.
type owner struct{ creds []webauthn.Credential }

func (o *owner) WebAuthnID() []byte                         { return []byte("badal-owner") }
func (o *owner) WebAuthnName() string                       { return "badal" }
func (o *owner) WebAuthnDisplayName() string                { return "badal" }
func (o *owner) WebAuthnCredentials() []webauthn.Credential { return o.creds }

type Service struct {
	pool *pgxpool.Pool
	wa   *webauthn.WebAuthn
}

// New builds the service. origin must be the exact https origin the PWA is
// served from; rpID is its registered domain.
func New(pool *pgxpool.Pool, rpID, origin string) (*Service, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "badal",
		RPID:          rpID,
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// Platform authenticator + resident key = Face ID / Touch ID with
			// no username to type.
			AuthenticatorAttachment: protocol.Platform,
			ResidentKey:             protocol.ResidentKeyRequirementPreferred,
			UserVerification:        protocol.VerificationPreferred,
		},
	})
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, wa: wa}, nil
}

func hash(t string) []byte { s := sha256.Sum256([]byte(t)); return s[:] }

func (s *Service) loadOwner(ctx context.Context) (*owner, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, public_key, attestation, aaguid, sign_count, transports,
		        backup_eligible, backup_state
		   FROM webauthn_credentials`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	o := &owner{}
	for rows.Next() {
		var c webauthn.Credential
		var transports []string
		if err := rows.Scan(&c.ID, &c.PublicKey, &c.AttestationType, &c.Authenticator.AAGUID,
			&c.Authenticator.SignCount, &transports, &c.Flags.BackupEligible, &c.Flags.BackupState); err != nil {
			return nil, err
		}
		for _, t := range transports {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
		o.creds = append(o.creds, c)
	}
	return o, rows.Err()
}

func (s *Service) HasCredentials(ctx context.Context) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials`).Scan(&n)
	return n > 0, err
}

func (s *Service) saveCeremony(ctx context.Context, purpose string, data any) (uuid.UUID, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx,
		`INSERT INTO webauthn_sessions (purpose, data, expires_at)
		 VALUES ($1,$2,now()+$3::interval) RETURNING id`,
		purpose, b, ceremonyTTL.String()).Scan(&id)
	return id, err
}

// takeCeremony consumes the challenge -- deleting on read makes replay
// impossible even if the response is captured.
func (s *Service) takeCeremony(ctx context.Context, id uuid.UUID, purpose string, out any) error {
	var b []byte
	err := s.pool.QueryRow(ctx,
		`DELETE FROM webauthn_sessions
		  WHERE id=$1 AND purpose=$2 AND expires_at > now()
		  RETURNING data`, id, purpose).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("challenge expired or already used")
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// ------------------------------------------------------------- registration

func (s *Service) BeginRegister(ctx context.Context) (any, uuid.UUID, error) {
	o, err := s.loadOwner(ctx)
	if err != nil {
		return nil, uuid.Nil, err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(o.creds))
	for _, c := range o.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sess, err := s.wa.BeginRegistration(o, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := s.saveCeremony(ctx, "register", sess)
	return opts, id, err
}

func (s *Service) FinishRegister(ctx context.Context, ceremony uuid.UUID, label string, r *http.Request) error {
	var sess webauthn.SessionData
	if err := s.takeCeremony(ctx, ceremony, "register", &sess); err != nil {
		return err
	}
	o, err := s.loadOwner(ctx)
	if err != nil {
		return err
	}
	cred, err := s.wa.FinishRegistration(o, sess, r)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	if label == "" {
		label = "passkey"
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO webauthn_credentials
		   (id, public_key, attestation, aaguid, sign_count, transports, backup_eligible, backup_state, label)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO NOTHING`,
		cred.ID, cred.PublicKey, cred.AttestationType, cred.Authenticator.AAGUID,
		cred.Authenticator.SignCount, transports, cred.Flags.BackupEligible, cred.Flags.BackupState, label)
	return err
}

// ------------------------------------------------------------------- login

func (s *Service) BeginLogin(ctx context.Context) (any, uuid.UUID, error) {
	o, err := s.loadOwner(ctx)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if len(o.creds) == 0 {
		return nil, uuid.Nil, fmt.Errorf("no passkeys registered")
	}
	opts, sess, err := s.wa.BeginLogin(o)
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := s.saveCeremony(ctx, "login", sess)
	return opts, id, err
}

// FinishLogin verifies the assertion and returns an opaque session token.
func (s *Service) FinishLogin(ctx context.Context, ceremony uuid.UUID, r *http.Request) (string, time.Time, error) {
	var sess webauthn.SessionData
	if err := s.takeCeremony(ctx, ceremony, "login", &sess); err != nil {
		return "", time.Time{}, err
	}
	o, err := s.loadOwner(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	cred, err := s.wa.FinishLogin(o, sess, r)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("verify: %w", err)
	}
	// A counter that fails to advance means the authenticator was cloned.
	// Authenticators that always report 0 are exempt -- that is legitimate.
	if cred.Authenticator.CloneWarning {
		return "", time.Time{}, fmt.Errorf("authenticator clone detected; credential rejected")
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE webauthn_credentials SET sign_count=$2, last_used_at=now() WHERE id=$1`,
		cred.ID, cred.Authenticator.SignCount)

	// Every session is backed by a device row so change_log attribution keeps
	// working -- without it, browser writes would have a null actor.
	var deviceID uuid.UUID
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO devices (name, platform, token_hash, scopes)
		 VALUES ($1,'web',$2,'{full}') RETURNING id`,
		browserName(r.UserAgent()), hash("session:"+uuid.NewString()),
	).Scan(&deviceID); err != nil {
		return "", time.Time{}, fmt.Errorf("session device: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	tok := "bs_" + base64.RawURLEncoding.EncodeToString(raw)
	exp := time.Now().Add(sessionTTL)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, credential_id, device_id, user_agent, expires_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		hash(tok), cred.ID, deviceID, r.UserAgent(), exp); err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// Authenticate validates a session token and returns its device id.
func (s *Service) Authenticate(ctx context.Context, tok string) (uuid.UUID, error) {
	if tok == "" {
		return uuid.Nil, ErrUnauthorized
	}
	var dev *uuid.UUID
	err := s.pool.QueryRow(ctx,
		`UPDATE sessions SET last_seen_at=now()
		  WHERE token_hash=$1 AND expires_at > now() RETURNING device_id`, hash(tok)).Scan(&dev)
	if err != nil || dev == nil {
		return uuid.Nil, ErrUnauthorized
	}
	return *dev, nil
}

// browserName gives sessions a recognisable label in the devices list, so
// revoking "iPhone" does not mean guessing between three UUIDs.
func browserName(ua string) string {
	switch {
	case contains(ua, "iPhone"):
		return "iPhone (passkey)"
	case contains(ua, "iPad"):
		return "iPad (passkey)"
	case contains(ua, "Android"):
		return "Android (passkey)"
	case contains(ua, "Macintosh"):
		return "Mac (passkey)"
	case contains(ua, "Windows"):
		return "Windows (passkey)"
	}
	return "Browser (passkey)"
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (s *Service) Logout(ctx context.Context, tok string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hash(tok))
	return err
}

func (s *Service) ListCredentials(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT encode(id,'base64'), label, created_at, last_used_at, backup_state
		   FROM webauthn_credentials ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, label string
		var created time.Time
		var used *time.Time
		var synced bool
		if err := rows.Scan(&id, &label, &created, &used, &synced); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "label": label, "created_at": created,
			"last_used_at": used, "synced": synced,
		})
	}
	return out, rows.Err()
}

func (s *Service) DeleteCredential(ctx context.Context, idB64 string) error {
	raw, err := base64.StdEncoding.DecodeString(idB64)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id=$1`, raw)
	return err
}

// GC clears expired ceremonies and sessions.
func (s *Service) GC(ctx context.Context) {
	_, _ = s.pool.Exec(ctx, `DELETE FROM webauthn_sessions WHERE expires_at < now()`)
	_, _ = s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
}

// SetCookie writes the session cookie. HttpOnly is the whole point: script
// cannot read it, so an XSS cannot exfiltrate the session.
func SetCookie(w http.ResponseWriter, tok string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    tok,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}
