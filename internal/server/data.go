package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/wesleygrimes/tuxosaurus/internal/identity"
)

const replayBytes = 8 << 20

type ticket struct {
	block     *block
	clientID  string
	expires   time.Time
	principal identity.Principal
}
type dataChannel struct {
	mu              sync.Mutex
	block           *block
	clientID, token string
	nonce           uint64
	buffer          []byte
	base            uint64
	notify          chan struct{}
	done            chan struct{}
	closed          bool
	conn            *websocket.Conn
	lostAt          time.Time
	principal       identity.Principal
}

func (s *Server) issueTicket(c *client, v *session, a object) (any, error) {
	if text(a, "data_ref_id") != "terminal" {
		return nil, missing("data reference not found")
	}
	if sub := text(a, "subprotocol"); sub != "" && sub != terminalProtocol {
		return nil, bad("unsupported data subprotocol")
	}
	b, err := v.findBlock(text(a, "block_id"))
	if err != nil {
		return nil, err
	}
	if b.remote != nil {
		return nil, missing("data reference not found")
	}
	for token, t := range s.tickets {
		if time.Now().After(t.expires) {
			delete(s.tickets, token)
		}
	}
	for token, d := range s.channels {
		d.mu.Lock()
		expired := d.closed || d.conn == nil && !d.lostAt.IsZero() && time.Since(d.lostAt) > time.Minute
		d.mu.Unlock()
		if expired {
			d.close()
			d.block.mu.Lock()
			delete(d.block.channels, d)
			d.block.mu.Unlock()
			delete(s.channels, token)
		}
	}
	if len(s.tickets) > 256 || len(s.channels) > 256 {
		return nil, bad("too many pending data connections")
	}
	token := secret()
	s.tickets[token] = ticket{block: b, clientID: c.id, expires: time.Now().Add(time.Minute), principal: c.principal}
	return object{"ticket": token, "expires_in_ms": 60000}, nil
}

func (d *dataChannel) append(p []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.buffer = append(d.buffer, p...)
	if len(d.buffer) > replayBytes {
		n := len(d.buffer) - replayBytes
		d.buffer = append([]byte(nil), d.buffer[n:]...)
		d.base += uint64(n)
	}
	select {
	case d.notify <- struct{}{}:
	default:
	}
}
func (d *dataChannel) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		close(d.done)
		if d.conn != nil {
			d.conn.CloseNow()
		}
	}
}

func (s *Server) data(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{terminalProtocol}, OriginPatterns: s.origins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helloCtx, helloCancel := context.WithTimeout(ctx, 5*time.Second)
	kind, hello, err := conn.Read(helloCtx)
	helloCancel()
	if err != nil {
		return
	}
	reject := func(reason byte) {
		conn.Write(ctx, websocket.MessageBinary, []byte{0x82, reason})
		conn.Close(websocket.StatusPolicyViolation, "data handshake rejected")
	}
	if kind != websocket.MessageBinary || len(hello) == 0 || conn.Subprotocol() != terminalProtocol {
		reject(7)
		return
	}
	s.mu.Lock()
	var d *dataChannel
	principal := identity.From(r)
	var offset uint64
	rejectReason := byte(1)
	switch {
	case hello[0] == 1 && len(hello) == 17:
		token := base64.RawURLEncoding.EncodeToString(hello[1:])
		t, ok := s.tickets[token]
		delete(s.tickets, token)
		if ok && time.Now().Before(t.expires) && t.principal.Key() == principal.Key() {
			b := t.block
			b.mu.Lock()
			if !b.closed {
				snapshot, snapshotErr := b.term.Snapshot()
				if snapshotErr != nil {
					rejectReason = 2
				}
				if snapshotErr == nil {
					var n [8]byte
					rand.Read(n[:])
					nonce := binary.LittleEndian.Uint64(n[:])
					if nonce == 0 {
						nonce = 1
					}
					d = &dataChannel{block: b, clientID: t.clientID, token: secret(), nonce: nonce, notify: make(chan struct{}, 1), done: make(chan struct{}), principal: principal}
					d.buffer = append(snapshot, b.resizeMarker(nonce)...)
					if b.theme != nil {
						d.buffer = append(d.buffer, b.themeMarker(nonce)...)
					}
					if b.exit != nil {
						d.buffer = append(d.buffer, b.childExitMarker(nonce)...)
					}
					b.channels[d] = true
					s.channels[d.token] = d
				}
			}
			b.mu.Unlock()
		}
	case hello[0] == 2 && len(hello) == 25:
		token := base64.RawURLEncoding.EncodeToString(hello[1:17])
		d = s.channels[token]
		offset = binary.LittleEndian.Uint64(hello[17:])
		if d != nil {
			d.mu.Lock()
			valid := !d.closed && d.conn == nil && time.Since(d.lostAt) < time.Minute && offset >= d.base && offset <= d.base+uint64(len(d.buffer)) && d.principal.Key() == principal.Key()
			d.mu.Unlock()
			if !valid {
				d = nil
			}
		}
	}
	if d != nil {
		d.mu.Lock()
		d.conn = conn
		d.mu.Unlock()
	}
	s.mu.Unlock()
	if d == nil {
		reject(rejectReason)
		return
	}
	defer func() {
		d.mu.Lock()
		if d.conn == conn {
			d.conn = nil
			d.lostAt = time.Now()
		}
		d.mu.Unlock()
	}()
	welcome := make([]byte, 32)
	welcome[0] = 0x81
	token, _ := base64.RawURLEncoding.DecodeString(d.token)
	copy(welcome[1:17], token)
	binary.LittleEndian.PutUint32(welcome[20:24], 60000)
	binary.LittleEndian.PutUint64(welcome[24:32], d.nonce)
	if err := conn.Write(ctx, websocket.MessageBinary, welcome); err != nil {
		return
	}
	// Terminal input uses the control API. A read loop handles ping/pong and
	// detects the disappearance of this output-only data connection.
	go func() {
		defer cancel()
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				return
			}
		}
	}()
	for {
		d.mu.Lock()
		if d.closed || offset < d.base {
			d.mu.Unlock()
			return
		}
		start := offset - d.base
		count := len(d.buffer) - int(start)
		if count > 64<<10 {
			count = 64 << 10
		}
		var chunk []byte
		if count > 0 {
			chunk = append([]byte(nil), d.buffer[int(start):int(start)+count]...)
		}
		d.mu.Unlock()
		if len(chunk) > 0 {
			if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
			offset += uint64(len(chunk))
			continue
		}
		select {
		case <-d.notify:
		case <-d.done:
			return
		case <-ctx.Done():
			return
		}
	}
}
