package protocol

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var serverBinary = flag.String("server-binary", "", "compatible server executable to test")
var testEndpoint = flag.String("endpoint", "", "already-running compatible server endpoint")

func startCompatible(t *testing.T, listen string) {
	t.Helper()
	root := t.TempDir()
	logfile, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "init.lua")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(*serverBinary, "--listen", listen, "--state-dir", filepath.Join(root, "state"), "--shell", "/bin/sh", "--config", config)
	cmd.Stdout, cmd.Stderr = logfile, logfile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("compatible server exited unsuccessfully: %v", err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("compatible server did not stop gracefully")
		}
		if t.Failed() {
			b, _ := os.ReadFile(logfile.Name())
			t.Logf("server log: %s", b)
		}
		logfile.Close()
	})
	t.Logf("server log: %s", logfile.Name())
}

type testClient struct {
	t        *testing.T
	ctx      context.Context
	conn     *websocket.Conn
	opts     *websocket.DialOptions
	base     string
	endpoint string
	n        int
	welcome  map[string]any
	events   []map[string]any
}

func dialCompatible(t *testing.T, ctx context.Context, endpoint string) *testClient {
	t.Helper()
	httpClient := http.DefaultClient
	base := "ws://" + strings.TrimPrefix(endpoint, "http://")
	if strings.HasPrefix(endpoint, "https://") {
		base = "wss://" + strings.TrimPrefix(endpoint, "https://")
	}
	if strings.HasPrefix(endpoint, "unix://") {
		path := strings.TrimPrefix(endpoint, "unix://")
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
		t.Cleanup(transport.CloseIdleConnections)
		httpClient = &http.Client{Transport: transport}
		base = "ws://rex"
	}
	var conn *websocket.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, _, err = websocket.Dial(ctx, base+"/api/control", &websocket.DialOptions{HTTPClient: httpClient, Subprotocols: []string{"rex.control.v1"}})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, map[string]any{"type": "hello", "client": map[string]any{"kind": "cli", "name": "compatibility test"}}); err != nil {
		t.Fatal(err)
	}
	var welcome map[string]any
	if err := wsjson.Read(ctx, conn, &welcome); err != nil {
		t.Fatal(err)
	}
	if welcome["protocol"] != "rex.control.v1" {
		t.Fatalf("welcome: %v", welcome)
	}
	return &testClient{t: t, ctx: ctx, conn: conn, base: base, endpoint: endpoint, opts: &websocket.DialOptions{HTTPClient: httpClient, Subprotocols: []string{"rex.data.terminal.v1"}}, welcome: welcome}
}

func (c *testClient) call(method string, payload any) map[string]any {
	c.t.Helper()
	result, failure := c.exchange(method, payload)
	if failure != nil {
		c.t.Fatalf("%s: %v", method, failure)
	}
	return result
}

func (c *testClient) exchange(method string, payload any) (map[string]any, map[string]any) {
	c.t.Helper()
	c.n++
	id := fmt.Sprint(c.n)
	if err := wsjson.Write(c.ctx, c.conn, map[string]any{"type": "request", "id": id, "method": method, "payload": payload}); err != nil {
		c.t.Fatal(err)
	}
	for {
		var frame map[string]any
		if err := wsjson.Read(c.ctx, c.conn, &frame); err != nil {
			c.t.Fatal(err)
		}
		if frame["type"] == "event" {
			c.events = append(c.events, frame["payload"].(map[string]any))
			continue
		}
		if frame["id"] != id {
			c.t.Fatalf("unexpected response: %v", frame)
		}
		failure, _ := frame["error"].(map[string]any)
		result, _ := frame["payload"].(map[string]any)
		return result, failure
	}
}

func (c *testClient) data(ticket string) (*websocket.Conn, []byte) {
	c.t.Helper()
	conn, _, err := websocket.Dial(c.ctx, c.base+"/api/data", c.opts)
	if err != nil {
		c.t.Fatal(err)
	}
	conn.SetReadLimit(16 << 20)
	c.t.Cleanup(func() { conn.CloseNow() })
	secret, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := conn.Write(c.ctx, websocket.MessageBinary, append([]byte{1}, secret...)); err != nil {
		c.t.Fatal(err)
	}
	_, welcome, err := conn.Read(c.ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	if len(welcome) != 32 || welcome[0] != 0x81 {
		c.t.Fatalf("data welcome: %x", welcome)
	}
	return conn, welcome
}

func TestCompatibility(t *testing.T) {
	if *serverBinary == "" && *testEndpoint == "" {
		t.Skip("requires -server-binary or -endpoint")
	}
	endpoint := *testEndpoint
	if endpoint == "" {
		f, err := os.CreateTemp("", "rex-compatible-*.sock")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		os.Remove(f.Name())
		t.Cleanup(func() { os.Remove(f.Name()) })
		endpoint = "unix://" + f.Name()
		startCompatible(t, endpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := dialCompatible(t, ctx, endpoint)
	checkMethodCatalog(t, c)
	checkBootstrap(t, c)
	checkHosts(t, c)
	checkClientCalls(t, c)
	checkClientQueue(t, c)
	checkClientLifecycle(t, c)
	checkClientScripts(t, c)
	checkTargets(t, c)
	checkTerminalOwnership(t, c)
	checkTerminalLifecycle(t, c)
	checkTerminalLaunch(t, c)
	checkTerminalMetadata(t, c)
	checkSelection(t, c)
	checkScrollback(t, c)
	checkProcessTracking(t, c)
	checkKeymapDetails(t, c)
	if *testEndpoint == "" {
		checkConfiguration(t, c)
	}
	checkLocalAdministration(t, c, strings.HasPrefix(endpoint, "unix://"))
	checkInputEncoding(t, c)
	checkBulkInput(t, c)
	checkTheme(t, c)
	checkListDir(t, c)
	checkClipboard(t, c)
	checkProgramStatus(t, c)
	checkLayout(t, c)
	checkLayoutEdges(t, c)
	checkKeymap(t, c.call)
	created := c.call("session.create", map[string]any{"label": "compatibility test", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sh"}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer func() {
		if ctx.Err() == nil {
			c.call("session.destroy", map[string]any{"session_id": sid})
		}
	}()
	attached := c.call("session.attach", map[string]any{"session_id": sid})
	view := attached["view"].(map[string]any)
	window := view["focused_window"].(map[string]any)
	bid := window["focused_block_id"].(string)
	args := func(args map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": args}
	}
	c.call("com.superlogical.terminal.resize", args(map[string]any{"columns": 100, "rows": 30}))
	ticket := c.call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
	dc, welcome := c.data(ticket["ticket"].(string))
	_, snapshot, err := dc.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(snapshot, []byte("GHOSTSNP")) {
		t.Fatalf("missing snapshot: %.30q", snapshot)
	}
	c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "paste", "payload": map[string]any{"text": "printf 'REX_%s_OK\\n' COMPAT"}}})
	// Modern Bash enables bracketed paste: a pasted newline is text, and a
	// separate Enter key submits it, just as in the native client.
	c.call("client.key", map[string]any{"session_id": sid, "block_id": bid, "key": map[string]any{"key": "enter", "text": "\r", "action": "press"}})
	var screen string
	for i := 0; i < 50; i++ {
		screen = fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"])
		if strings.Contains(screen, "REX_COMPAT_OK") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(screen, "REX_COMPAT_OK") {
		t.Fatalf("PTY output missing: %q", screen)
	}
	// The native app uses client.key for ordinary typing, rather than paste.
	c.call("client.key", map[string]any{"session_id": sid, "block_id": bid, "key": map[string]any{"key": "unidentified", "text": "printf 'REX_%s_OK\\n' KEY", "mods": "", "consumed_mods": "", "action": "press", "unshifted_codepoint": 0, "composing": false}})
	c.call("client.key", map[string]any{"session_id": sid, "block_id": bid, "key": map[string]any{"key": "enter", "text": "\r", "mods": "", "consumed_mods": "", "action": "press", "unshifted_codepoint": 13, "composing": false}})
	for i := 0; i < 50; i++ {
		screen = fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"])
		if strings.Contains(screen, "REX_KEY_OK") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(screen, "REX_KEY_OK") {
		t.Fatalf("client.key did not reach the PTY: %q", screen)
	}
	// Close both connections, then attach from a completely new client. The
	// same shell and its screen must remain alive.
	dc.CloseNow()
	c.conn.CloseNow()
	c2 := dialCompatible(t, ctx, endpoint)
	c = c2
	c.call("session.attach", map[string]any{"session_id": sid})
	if got := fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"]); !strings.Contains(got, "REX_COMPAT_OK") {
		t.Fatalf("screen was lost after disconnect: %q", got)
	}
	// Resume from the byte offset received before disconnect. The output
	// produced in the meantime must be replayed without a replacement snapshot.
	rc, _, err := websocket.Dial(ctx, c.base+"/api/data", c.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.CloseNow()
	resume := make([]byte, 25)
	resume[0] = 2
	copy(resume[1:17], welcome[1:17])
	binary.LittleEndian.PutUint64(resume[17:], uint64(len(snapshot)))
	if err := rc.Write(ctx, websocket.MessageBinary, resume); err != nil {
		t.Fatal(err)
	}
	_, resumed, err := rc.Read(ctx)
	if err != nil || len(resumed) != 32 || resumed[0] != 0x81 {
		t.Fatalf("resume handshake: %x, %v", resumed, err)
	}
	var replay []byte
	for !bytes.Contains(replay, []byte("REX_COMPAT_OK")) {
		_, chunk, err := rc.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		replay = append(replay, chunk...)
	}
	if bytes.HasPrefix(replay, []byte("GHOSTSNP")) {
		t.Fatal("resume restarted the terminal instead of replaying the missing bytes")
	}
	// A fresh connection must receive a snapshot of the current terminal.
	ticket = c.call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
	dc, _ = c.data(ticket["ticket"].(string))
	_, restored, err := dc.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(restored, []byte("GHOSTSNP")) {
		t.Fatal("reattach did not receive a snapshot")
	}
	if binary.LittleEndian.Uint32(welcome[20:24]) != 60000 {
		t.Fatal("wrong reconnect grace")
	}
	if *rexBinary != "" {
		// Check the snapshot with the Rex CLI's independent decoder.
		cmd := exec.CommandContext(ctx, *rexBinary, "--autostart=false", "--server", endpoint, "capture", "-s", sid, "-b", bid)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Rex capture: %v: %s", err, out)
		}
		if !bytes.Contains(out, []byte("REX_COMPAT_OK")) {
			t.Fatalf("Rex could not restore the terminal: %q", out)
		}
	}
}

func checkLocalAdministration(t *testing.T, c *testClient, local bool) {
	t.Helper()
	who := c.call("server.whoami", map[string]any{})
	p, _ := who["principal"].(map[string]any)
	if who["account"] == "" {
		t.Fatalf("executing OS account is missing: %v", who)
	}
	if local {
		if p["kind"] != "peer" || p["transport"] != "unix" || p["uid"] != float64(os.Getuid()) || p["gid"] != float64(os.Getgid()) {
			t.Fatalf("Unix peer credentials: %v", who)
		}
		for _, method := range []string{"server.tailscale.disable", "server.tailscale.logout"} {
			got := c.call(method, map[string]any{})
			if got["tailscale"].(map[string]any)["state"] != "disabled" {
				t.Fatalf("%s: %v", method, got)
			}
		}
	} else {
		for _, method := range []string{"server.tailscale.enable", "server.tailscale.disable", "server.tailscale.login", "server.tailscale.logout", "server.tailscale.rename"} {
			_, failure := c.exchange(method, map[string]any{})
			if failure["status"] != float64(403) {
				t.Fatalf("remote administration was not denied for %s: %v", method, failure)
			}
		}
	}
}

func checkKeymap(t *testing.T, call func(string, any) map[string]any) {
	t.Helper()
	key := []any{"cmd+n"}
	got := call("client.keymap", map[string]any{
		"actions":        []any{map[string]any{"name": "session.new", "kind": "client"}, map[string]any{"name": "client.window.new", "kind": "client"}},
		"server_actions": "configured",
		"defaults":       []any{map[string]any{"name": "defaults", "bindings": []any{map[string]any{"keys": key, "action": "session.new"}, map[string]any{"keys": key, "action": "client.window.new"}}}},
		"overrides":      []any{map[string]any{"name": "settings", "removals": []any{map[string]any{"keys": key, "action": "session.new"}}, "bindings": []any{map[string]any{"keys": []any{"cmd+e"}, "action": "session.new"}}}},
	})
	bindings, _ := got["bindings"].([]any)
	if len(bindings) != 2 || bindings[0].(map[string]any)["action"] != "session.new" || bindings[1].(map[string]any)["action"] != "client.window.new" {
		t.Fatalf("wrong key binding precedence or removal: %v", got)
	}
	if actions, _ := got["actions"].([]any); len(actions) != 0 {
		t.Fatalf("client actions were returned as configured server actions: %v", actions)
	}
}
