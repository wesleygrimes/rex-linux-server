package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/wesleygrimes/tuxosaurus/internal/identity"
	"github.com/wesleygrimes/tuxosaurus/internal/state"
	"github.com/wesleygrimes/tuxosaurus/internal/tailnet"
)

type object = map[string]any

const controlProtocol = "rex.control.v1"
const terminalProtocol = "rex.data.terminal.v1"

type Server struct {
	mu                        sync.Mutex
	hostID, instanceID, shell string
	started                   time.Time
	sessions                  []*session
	clients                   map[string]*client
	tickets                   map[string]ticket
	channels                  map[string]*dataChannel
	closed                    bool
	exitEmpty                 bool
	origins                   []string
	limits                    Limits
	plugins                   []*blockPlugin
	creators                  map[string]*pluginCreator
	lifetime                  context.Context
	cancelLifetime            context.CancelFunc
	pluginWorkers             sync.WaitGroup
	tailnet                   *tailnet.Node
	account                   string
	stateDir                  string
	pendingCalls              map[string]*clientCall
	configPath                string
	configVM                  *scriptVM
	configGeneration          int
	configExists              bool
	configDigest              string
	configDiagnostics         []any
	configLoad                sync.Mutex
	unlock                    func()
}

type client struct {
	id, token    string
	info         object
	seq          uint64
	attached     map[string]bool
	all          bool
	subscribed   bool
	out          *controlQueue
	conn         *websocket.Conn
	lostAt       time.Time
	principal    identity.Principal
	connectedAt  time.Time
	actions      []any
	done         <-chan struct{}
	performing   bool
	performQueue []*performRequest
	script       *scriptVM
}

type request struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

type apiError struct {
	Code, Message string
	Status        int
}

func (e *apiError) Error() string  { return e.Message }
func bad(message string) error     { return &apiError{"bad_request", message, 400} }
func missing(message string) error { return &apiError{"not_found", message, 404} }

func New(stateDir, shell string, configPath ...string) (*Server, error) {
	unlock, err := state.Lock(stateDir)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			unlock()
		}
	}()
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	s := &Server{hostID: uuid(), instanceID: uuid(), shell: shell, stateDir: stateDir, limits: Limits{Sessions: 64, Blocks: 1024, Windows: 64, Panes: 64}, creators: map[string]*pluginCreator{}, started: time.Now(), clients: map[string]*client{}, tickets: map[string]ticket{}, channels: map[string]*dataChannel{}, pendingCalls: map[string]*clientCall{}}
	s.lifetime, s.cancelLifetime = context.WithCancel(context.Background())
	defer func() {
		if !ready {
			s.cancelLifetime()
		}
	}()
	if u, err := user.Current(); err == nil {
		s.account = u.Username
	}
	if stateDir != "" {
		if err := os.MkdirAll(stateDir, 0700); err != nil {
			return nil, err
		}
		path := filepath.Join(stateDir, "host-id")
		b, err := os.ReadFile(path)
		if err == nil {
			s.hostID = strings.TrimSpace(string(b))
			if s.hostID == "" {
				return nil, fmt.Errorf("empty host identity: %s", path)
			}
		} else if os.IsNotExist(err) {
			if err = os.WriteFile(path, []byte(s.hostID+"\n"), 0600); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	node, err := tailnet.New(stateDir, s.Handler(), s.tailnetChanged)
	if err != nil {
		return nil, err
	}
	s.tailnet = node
	s.unlock = unlock
	s.configPath = defaultConfigPath()
	if len(configPath) > 0 && configPath[0] != "" {
		s.configPath = configPath[0]
	}
	s.loadConfig(context.Background())
	ready = true
	return s, nil
}

func (s *Server) Tailnet() *tailnet.Node { return s.tailnet }

func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func secret() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func id(kind string) string          { return kind + ":" + uuid() }
func text(o object, k string) string { v, _ := o[k].(string); return v }
func number(o object, k string, def int) int {
	if v, ok := o[k].(float64); ok {
		return int(v)
	}
	return def
}
func asObject(v any) object {
	o, _ := v.(map[string]any)
	if o == nil {
		return object{}
	}
	return o
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/control", s.control)
	mux.HandleFunc("GET /api/data", s.data)
	mux.HandleFunc("GET /api/help", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		catalog := s.help()
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(catalog)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(object{"status": "ok"})
	})
	mux.HandleFunc("GET /llms.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(apiGuide)
	})
	mux.HandleFunc("GET /llms.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Tuxosaurus API</title><body><pre>%s</pre></body></html>", html.EscapeString(string(apiGuide)))
	})
	return mux
}

// Configure before accepting connections.
func (s *Server) ConfigureRun(exitEmpty bool, origins []string) {
	s.exitEmpty = exitEmpty
	s.origins = append([]string(nil), origins...)
}

func (s *Server) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Lost control streams retain their reconnect slot for the grace period.
	return len(s.sessions) == 0 && len(s.clients) == 0
}

func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	vms := []*scriptVM{s.configVM}
	for _, c := range s.clients {
		vms = append(vms, c.script)
		if c.conn != nil {
			c.conn.CloseNow()
		}
	}
	for _, d := range s.channels {
		d.close()
	}
	for _, session := range s.sessions {
		for _, b := range session.blocks {
			b.close()
		}
	}
	for _, vm := range vms {
		if vm != nil {
			vm.cancel()
		}
	}
	s.cancelLifetime()
	s.mu.Unlock()
	for _, plugin := range s.plugins {
		plugin.close()
	}
	s.pluginWorkers.Wait()
	for _, vm := range vms {
		if vm != nil {
			vm.close()
		}
	}
	s.tailnet.Close()
	s.unlock()
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{controlProtocol}, OriginPatterns: s.origins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != controlProtocol {
		conn.Close(websocket.StatusProtocolError, "control subprotocol rex.control.v1 is required")
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helloCtx, helloCancel := context.WithTimeout(ctx, 5*time.Second)
	var hello struct {
		Type           string `json:"type"`
		ReconnectToken string `json:"reconnect_token"`
		Client         object `json:"client"`
	}
	err = wsjson.Read(helloCtx, conn, &hello)
	helloCancel()
	if err != nil || hello.Type != "hello" {
		conn.Close(websocket.StatusProtocolError, "expected hello")
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	var c *client
	principal := identity.From(r)
	resumed := false
	for token, old := range s.clients {
		if old.conn == nil && time.Since(old.lostAt) > 60*time.Second {
			delete(s.clients, token)
			if old.script != nil {
				old.script.cancel()
				go old.script.close()
			}
			s.clientEventLocked("client_left", object{"client_id": old.id})
			continue
		}
		if hello.ReconnectToken != "" && token == hello.ReconnectToken && old.conn == nil && principal.Key() == old.principal.Key() {
			c = old
			resumed = true
		}
	}
	if c == nil {
		c = &client{id: id("client"), token: secret(), attached: map[string]bool{}, principal: principal, connectedAt: time.Now()}
		s.clients[c.token] = c
	}
	c.info = sanitizeClientInfo(hello.Client)
	c.conn = conn
	c.done = ctx.Done()
	c.out = newControlQueue()
	out := c.out
	event := "client_joined"
	if resumed {
		event = "client_changed"
	}
	s.clientEventLocked(event, object{"client": s.clientInfo(c)})
	if resumed {
		for sid := range c.attached {
			if v, err := s.findSession(sid); err == nil {
				v.notifyRemote("client_connection_changed", object{"client_id": c.id, "connection_state": "connected"})
			}
		}
	}
	clientWelcome := object{"id": c.id, "reconnect_token": c.token}
	if resumed {
		clientWelcome["resumed"] = true
	}
	welcome := object{"type": "welcome", "protocol": controlProtocol, "limits": object{"max_message_bytes": 1 << 20}, "client": clientWelcome}
	s.mu.Unlock()
	if err := wsjson.Write(ctx, conn, welcome); err != nil {
		return
	}
	defer func() {
		s.mu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.lostAt = time.Now()
			s.releaseClientSizes(c, "")
			s.clientEventLocked("client_changed", object{"client": s.clientInfo(c)})
			for sid := range c.attached {
				if v, err := s.findSession(sid); err == nil {
					v.notifyRemote("client_connection_changed", object{"client_id": c.id, "connection_state": "lost"})
				}
			}
			lostAt := c.lostAt
			time.AfterFunc(60*time.Second, func() { s.expireClient(c, lostAt) })
		}
		s.mu.Unlock()
	}()
	go func() {
		for {
			data, ok := out.next(ctx)
			if !ok {
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				cancel()
				conn.CloseNow()
				return
			}
		}
	}()
	log.Printf("control connected: %s", c.id)
	ordered := make(chan request, 256)
	go func() {
		previous := ""
		for {
			select {
			case req := <-ordered:
				s.serveRequest(ctx, c, conn, out, req, previous)
				previous = req.ID
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		var req request
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			log.Printf("control disconnected: %s: %v", c.id, err)
			return
		}
		if req.Type != "request" || req.ID == "" || req.Method == "" {
			conn.Close(websocket.StatusProtocolError, "invalid request")
			return
		}
		if req.Method == "client.key" || req.Method == "client.action" {
			select {
			case ordered <- req:
			case <-ctx.Done():
				return
			}
		} else {
			go s.serveRequest(ctx, c, conn, out, req, "")
		}
	}
}

func (s *Server) serveRequest(ctx context.Context, c *client, conn *websocket.Conn, out *controlQueue, req request, previous string) {
	args := object{}
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &args); err != nil || args == nil {
			conn.Close(websocket.StatusProtocolError, "payload must be an object")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || c.conn != conn || ctx.Err() != nil {
		return
	}
	log.Printf("control request: %s %s", c.id, req.Method)
	var payload any
	var err error
	if req.Method == "client.perform" {
		s.mu.Unlock()
		payload, err = s.performClient(ctx, c, args)
		s.mu.Lock()
	} else if req.Method == "client.set_script" || req.Method == "client.action" || req.Method == "client.key" || req.Method == "server.config_reload" {
		s.mu.Unlock()
		payload, err = s.scriptRequest(ctx, c, req.Method, args)
		s.mu.Lock()
	} else {
		payload, err = s.dispatch(c, req.Method, args)
	}
	if c.conn != conn || ctx.Err() != nil {
		return
	}
	if req.Method == "client.action" && previous != "" && err == nil {
		payload.(object)["ordered_after"] = previous
	}
	response := object{"type": "response", "id": req.ID, "seq": c.seq}
	if err != nil {
		e, ok := err.(*apiError)
		if !ok {
			e = &apiError{"internal", err.Error(), 500}
		}
		response["error"] = object{"code": e.Code, "message": e.Message, "status": e.Status}
		log.Printf("%s: %s", req.Method, err)
	} else {
		response["payload"] = payload
	}
	if !out.push(response) {
		conn.CloseNow()
	}
}

// emitLocked queues events in the same order as responses on each control stream.
func (s *Server) emitLocked(kind string, payload any, sessionID string) {
	for _, c := range s.clients {
		if c.conn == nil || (kind == "session_event" && !c.all && !c.attached[sessionID]) || (kind != "session_event" && !c.subscribed && kind != "hosts_changed" && kind != "keymap_changed") {
			continue
		}
		c.seq++
		frame := object{"type": "event", "seq": c.seq, "payload": object{"type": kind, "payload": payload}}
		if !c.out.push(frame) {
			c.conn.CloseNow()
		}
	}
}
func (s *Server) sessionEvent(sessionID, kind string, payload any) {
	s.emitLocked("session_event", object{"session_id": sessionID, "event": object{"type": kind, "payload": payload}}, sessionID)
	if kind == "block_event" && !s.closed {
		if v, err := s.findSession(sessionID); err == nil {
			for _, b := range v.blocks {
				if b.remote != nil && !b.remote.creator.process.contextEvents.push(object{"block_id": b.remote.id, "event": object{"type": kind, "payload": payload}}) {
					b.remote.creator.process.cancel()
				}
			}
		}
	}
}
func (s *Server) status() object {
	return object{"pid": os.Getpid(), "instance_id": s.instanceID, "host_id": s.hostID, "started_at": s.started, "exit_empty": s.exitEmpty, "session_count": len(s.sessions), "tailscale": s.tailnet.Status(), "config": object{"generation": s.configGeneration, "hash": s.configDigest}}
}
func (s *Server) version() object {
	return object{"rex_version": "0.1.0-compat", "platform": runtime.GOOS + "_" + runtime.GOARCH, "dirty": false}
}
