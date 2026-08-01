// Package voice relays spoken audio between the Mac and the phone.
//
// The job queue cannot carry this. It polls, which costs up to a poll interval
// before work is even seen, and it returns one result at the end -- so a
// sentence would arrive as a finished file seconds after it was asked for.
// Speech has to start playing while it is still being made, and it has to stop
// the instant it is interrupted. Both need a connection that stays open.
//
// So: two websockets meeting in the middle. The Mac holds one and waits for
// text; the phone opens one and receives PCM. The VPS is a relay and keeps no
// audio -- nothing is buffered here beyond what is in flight.
//
// Exactly one speaker at a time, deliberately. This is one person's phone
// talking to one person's laptop, and a second listener would only ever be the
// same person on another device, who should take over rather than share.
package voice

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Frame types on the wire from worker to client. One byte, so the client can
// switch on it without parsing anything.
const (
	FramePCM   = 0 // signed 16-bit little-endian, 24kHz mono
	FrameDone  = 1 // end of an utterance
	FrameError = 2 // utf-8 message
)

type Hub struct {
	mu     sync.Mutex
	worker *conn // the Mac, when connected
	client *conn // the phone, when listening

	// Set when the listener interrupts, cleared when the utterance being
	// abandoned reports done.
	//
	// Cancelling at the source cannot work on its own. Kokoro generates a
	// whole sentence in a second or so and then emits every frame of it in
	// about a millisecond, so an interruption sent from a phone arrives long
	// after the audio has already been written. Gating here stops it reaching
	// anyone the instant the cancel lands, whatever the engine is doing.
	muted bool
}

type conn struct {
	ws   *websocket.Conn
	send chan outgoing
	done chan struct{}
}

type outgoing struct {
	typ  websocket.MessageType
	data []byte
}

func NewHub() *Hub { return &Hub{} }

// WorkerOnline reports whether speech can be produced on the Mac. The client
// asks before connecting so it can fall back to the browser's own voice
// without waiting for a timeout.
func (h *Hub) WorkerOnline() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.worker != nil
}

func newConn(ws *websocket.Conn) *conn {
	return &conn{ws: ws, send: make(chan outgoing, 64), done: make(chan struct{})}
}

// pump writes queued messages until the connection dies. Writing from one
// goroutine only is a requirement of the library, and it also means a slow
// reader cannot block the relay.
func (c *conn) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case m := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Write(wctx, m.typ, m.data)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (c *conn) push(typ websocket.MessageType, data []byte) {
	select {
	case c.send <- outgoing{typ, data}:
	default:
		// A listener too slow to keep up with speech is not worth stalling the
		// relay for; dropping a frame costs a click, blocking costs the call.
		slog.Warn("voice: dropping frame, receiver is behind")
	}
}

// ---------------------------------------------------------------- worker

// ServeWorker registers the Mac and relays its audio to whoever is listening.
func (h *Hub) ServeWorker(ctx context.Context, ws *websocket.Conn) {
	// Speech frames are small and frequent; a generous cap is only there to
	// stop a broken peer allocating without bound.
	ws.SetReadLimit(4 << 20)
	c := newConn(ws)

	h.mu.Lock()
	if old := h.worker; old != nil {
		close(old.done) // a reconnect replaces the stale registration
	}
	h.worker = c
	h.mu.Unlock()
	slog.Info("voice: worker connected")

	go c.pump(ctx)
	defer func() {
		h.mu.Lock()
		if h.worker == c {
			h.worker = nil
		}
		h.mu.Unlock()
		slog.Info("voice: worker gone")
		ws.CloseNow()
	}()

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		h.mu.Lock()
		cl, muted := h.client, h.muted
		// A done frame ends the abandoned utterance; whatever follows was
		// asked for after the interruption and is wanted.
		if muted && len(data) > 0 && data[0] == FrameDone {
			h.muted = false
		}
		h.mu.Unlock()
		if cl != nil && !muted {
			cl.push(typ, data)
		}
	}
}

// ---------------------------------------------------------------- client

type clientMsg struct {
	Say    string `json:"say,omitempty"`
	Cancel bool   `json:"cancel,omitempty"`
	Voice  string `json:"voice,omitempty"`
}

// ServeClient takes text from the phone and hands it to the Mac.
func (h *Hub) ServeClient(ctx context.Context, ws *websocket.Conn) {
	ws.SetReadLimit(256 << 10)
	c := newConn(ws)

	h.mu.Lock()
	if old := h.client; old != nil {
		close(old.done) // the newest device takes over
	}
	h.client = c
	worker := h.worker
	h.muted = false
	h.mu.Unlock()

	go c.pump(ctx)
	defer func() {
		h.mu.Lock()
		if h.client == c {
			h.client = nil
		}
		h.mu.Unlock()
		ws.CloseNow()
	}()

	// Say up front which voice will be used, so the client can switch to the
	// browser without waiting to discover that nothing is coming.
	hello, _ := json.Marshal(map[string]any{"provider": providerName(worker)})
	c.push(websocket.MessageText, hello)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			// Leaving stops the Mac talking. Otherwise a closed tab would keep
			// a laptop generating audio nobody will hear.
			h.toWorker(clientMsg{Cancel: true})
			return
		}
		var m clientMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Cancel {
			h.mu.Lock()
			h.muted = true
			h.mu.Unlock()
		} else if m.Say != "" {
			// Asking for something new implies the last thing is no longer
			// wanted, and the client already stopped playing it.
			h.mu.Lock()
			h.muted = false
			h.mu.Unlock()
		}
		if !h.toWorker(m) {
			out, _ := json.Marshal(map[string]any{"provider": "browser"})
			c.push(websocket.MessageText, out)
		}
	}
}

func (h *Hub) toWorker(m clientMsg) bool {
	h.mu.Lock()
	w := h.worker
	h.mu.Unlock()
	if w == nil {
		return false
	}
	b, _ := json.Marshal(m)
	w.push(websocket.MessageText, b)
	return true
}

func providerName(w *conn) string {
	if w == nil {
		return "browser"
	}
	return "mac"
}
