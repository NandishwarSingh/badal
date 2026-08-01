package voice

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Short-lived tickets, because a browser WebSocket cannot carry a header.
//
// new WebSocket(url) takes a URL and nothing else: no Authorization, no body.
// A passkey session rides on a cookie and works, but a device token lives in
// localStorage and has no way in -- which is why every phone got a 401 while
// the laptop connected fine.
//
// Putting the device token in the query string would solve it and put a
// long-lived credential into every access log and proxy trace. A ticket is
// single-use and lives thirty seconds, so a leaked URL is worth nothing by the
// time anyone reads it.
const ticketTTL = 30 * time.Second

type tickets struct {
	mu sync.Mutex
	m  map[string]time.Time
}

var store = tickets{m: map[string]time.Time{}}

func NewTicket() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	t := base64.RawURLEncoding.EncodeToString(b)

	store.mu.Lock()
	defer store.mu.Unlock()
	// Opportunistic sweep: this is called once per connection attempt, so
	// there is no need for a timer goroutine to keep the map from growing.
	now := time.Now()
	for k, exp := range store.m {
		if now.After(exp) {
			delete(store.m, k)
		}
	}
	store.m[t] = now.Add(ticketTTL)
	return t
}

// RedeemTicket consumes a ticket. A second attempt with the same one fails,
// so a URL captured in a log cannot be replayed.
func RedeemTicket(t string) bool {
	if t == "" {
		return false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	exp, ok := store.m[t]
	if !ok {
		return false
	}
	delete(store.m, t)
	return time.Now().Before(exp)
}
