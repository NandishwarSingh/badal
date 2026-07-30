// Package push delivers Web Push notifications to subscribed browsers.
//
// This is what replaces APNs. On iOS 16.4+ a home-screen-installed PWA gets
// real push with no Apple Developer account, which is why the UI insists on
// installing before it offers notifications.
package push

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool    *pgxpool.Pool
	pub     string
	priv    string
	subject string
}

// New loads the VAPID keypair, generating it once on first boot.
//
// The keys must never rotate: the public key is baked into every browser
// subscription, so regenerating them silently breaks every existing device
// until it re-subscribes.
func New(ctx context.Context, pool *pgxpool.Pool, subject string) (*Service, error) {
	s := &Service{pool: pool, subject: subject}
	err := pool.QueryRow(ctx,
		`SELECT public_key, private_key, subject FROM push_config WHERE id=1`).
		Scan(&s.pub, &s.priv, &s.subject)
	if err == nil {
		return s, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, fmt.Errorf("generate vapid: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO push_config (id, public_key, private_key, subject)
		 VALUES (1,$1,$2,$3) ON CONFLICT (id) DO NOTHING`, pub, priv, subject); err != nil {
		return nil, err
	}
	// Re-read: a concurrent boot may have won the insert, and using a
	// different keypair than the stored one would break every subscription.
	if err := pool.QueryRow(ctx,
		`SELECT public_key, private_key, subject FROM push_config WHERE id=1`).
		Scan(&s.pub, &s.priv, &s.subject); err != nil {
		return nil, err
	}
	slog.Info("vapid keypair ready")
	return s, nil
}

func (s *Service) PublicKey() string { return s.pub }

// vapidSubscriber normalises the VAPID `sub` claim.
//
// webpush-go prepends "mailto:" to anything that is not an https URL, so
// passing an already-prefixed address yields sub="mailto:mailto:you@example
// .com". Firefox tolerates that; Apple rejects the whole JWT with
// {"reason":"BadJwtToken"} and no further explanation.
func vapidSubscriber(subject string) string {
	if strings.HasPrefix(subject, "https:") {
		return subject
	}
	return strings.TrimPrefix(subject, "mailto:")
}

type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Service) Subscribe(ctx context.Context, sub Subscription, deviceID *uuid.UUID, ua string) error {
	if sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return fmt.Errorf("incomplete subscription")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO push_subscriptions (endpoint, p256dh, auth, device_id, user_agent)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (endpoint) DO UPDATE
		   SET p256dh=EXCLUDED.p256dh, auth=EXCLUDED.auth, failed_at=NULL`,
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, deviceID, ua)
	return err
}

func (s *Service) Unsubscribe(ctx context.Context, endpoint string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint=$1`, endpoint)
	return err
}

func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM push_subscriptions WHERE failed_at IS NULL`).Scan(&n)
	return n, err
}

type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// Send delivers to every live subscription. Returns how many succeeded.
func (s *Service) Send(ctx context.Context, m Message) (int, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, endpoint, p256dh, auth FROM push_subscriptions WHERE failed_at IS NULL`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id                       uuid.UUID
		endpoint, p256dh, secret string
	}
	var subs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.endpoint, &r.p256dh, &r.secret); err != nil {
			rows.Close()
			return 0, err
		}
		subs = append(subs, r)
	}
	rows.Close()

	sent := 0
	for _, r := range subs {
		sub := &webpush.Subscription{
			Endpoint: r.endpoint,
			Keys:     webpush.Keys{P256dh: r.p256dh, Auth: r.secret},
		}
		resp, err := webpush.SendNotificationWithContext(ctx, payload, sub, &webpush.Options{
			Subscriber:      vapidSubscriber(s.subject),
			VAPIDPublicKey:  s.pub,
			VAPIDPrivateKey: s.priv,
			TTL:             86400,
			Urgency:         webpush.UrgencyNormal,
		})
		if err != nil {
			slog.Warn("push send", "endpoint", short(r.endpoint), "err", err)
			continue
		}
		code := resp.StatusCode
		// Push services put the actual reason in the body. Discarding it turns
		// a fixable 403 into a mystery.
		var reason string
		if code < 200 || code >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			reason = strings.TrimSpace(string(b))
		}
		resp.Body.Close()
		// 404/410 are the push service telling us this endpoint is dead for
		// good. Retrying those forever is how a push queue rots.
		if code == http.StatusNotFound || code == http.StatusGone {
			_, _ = s.pool.Exec(ctx,
				`UPDATE push_subscriptions SET failed_at=now() WHERE id=$1`, r.id)
			slog.Info("push subscription expired", "endpoint", short(r.endpoint), "reason", reason)
			continue
		}
		if code >= 200 && code < 300 {
			sent++
			continue
		}
		slog.Warn("push rejected", "status", code, "endpoint", short(r.endpoint), "reason", reason)
	}
	return sent, nil
}

func short(s string) string {
	if len(s) > 48 {
		return s[:48] + "…"
	}
	return s
}

// PurgeFailed drops endpoints that have been dead for a week.
func (s *Service) PurgeFailed(ctx context.Context) {
	_, _ = s.pool.Exec(ctx,
		`DELETE FROM push_subscriptions WHERE failed_at IS NOT NULL AND failed_at < now() - interval '7 days'`)
	_ = time.Now
}
