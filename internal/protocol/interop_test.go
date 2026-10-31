package protocol

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
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

var rexBinary = flag.String("rex-binary", "", "path to an installed Rex executable for interoperability checks")
var captureDir = flag.String("capture-dir", "", "directory for interoperability test output")

func TestRexInterop(t *testing.T) {
	if *rexBinary == "" {
		t.Skip("requires -rex-binary pointing to an installed Rex executable")
	}
	root := t.TempDir()
	// A short path keeps macOS Unix socket paths below sockaddr_un's limit.
	socket, err := os.CreateTemp("", "rex-probe-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket.Close()
	os.Remove(socket.Name())
	t.Cleanup(func() { os.Remove(socket.Name()) })
	endpoint := "unix://" + socket.Name()
	config := filepath.Join(root, "empty.lua")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "XDG_CONFIG_HOME="+root, "XDG_DATA_HOME="+root, "XDG_CACHE_HOME="+root, "REX_RUNTIME_DIR="+root)
	logfile, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logfile.Close()
	cmd := exec.Command(*rexBinary, "server", "run", "--listen", endpoint, "--state-dir", filepath.Join(root, "state"), "--config", config)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, logfile, logfile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(logfile.Name())
			t.Logf("Rex interoperability log: %s", b)
		}
		cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(socket.Name()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logfile.Name())
			t.Fatalf("Rex server did not start: %s", b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if *captureDir != "" {
		discover := exec.CommandContext(ctx, *rexBinary, "--autostart=false", "--server", endpoint, "api", "list", "--json")
		discover.Env = env
		b, err := discover.CombinedOutput()
		if err != nil {
			t.Fatalf("api discovery: %v: %s", err, b)
		}
		os.MkdirAll(*captureDir, 0700)
		os.WriteFile(filepath.Join(*captureDir, "api.json"), b, 0600)
		t.Logf("captured %d bytes of API schemas", len(b))
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket.Name())
	}}
	defer transport.CloseIdleConnections()
	c, _, err := websocket.Dial(ctx, "ws://rex/api/control", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{"rex.control.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := wsjson.Write(ctx, c, map[string]any{"type": "hello", "client": map[string]any{"kind": "cli", "name": "protocol probe"}}); err != nil {
		t.Fatal(err)
	}
	var welcome json.RawMessage
	if err := wsjson.Read(ctx, c, &welcome); err != nil {
		t.Fatal(err)
	}
	t.Logf("welcome: %s", welcome)
	save := func(name string, b []byte) {
		if *captureDir == "" {
			return
		}
		if err := os.MkdirAll(*captureDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(*captureDir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save("welcome.json", welcome)
	call := func(method string, payload any) map[string]any {
		if err := wsjson.Write(ctx, c, map[string]any{"type": "request", "id": method, "method": method, "payload": payload}); err != nil {
			t.Fatal(err)
		}
		for {
			var raw json.RawMessage
			if err := wsjson.Read(ctx, c, &raw); err != nil {
				t.Fatal(err)
			}
			var frame struct {
				Type    string
				ID      string
				Payload map[string]any
				Error   any
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Type == "event" {
				t.Logf("event: %.3000s", raw)
				continue
			}
			save(strings.ReplaceAll(method, ".", "-")+".json", raw)
			t.Logf("%s: %.4000s", method, raw)
			if frame.Error != nil {
				t.Fatalf("%s: %v", method, frame.Error)
			}
			return frame.Payload
		}
	}
	probe := dialCompatible(t, ctx, endpoint)
	checkMethodCatalog(t, probe)
	checkBootstrap(t, probe)
	checkHosts(t, probe)
	checkClientCalls(t, probe)
	checkClientQueue(t, probe)
	checkClientLifecycle(t, probe)
	checkClientScripts(t, probe)
	checkConfiguration(t, probe)
	checkTargets(t, probe)
	checkTerminalOwnership(t, probe)
	checkTerminalLifecycle(t, probe)
	checkTerminalLaunch(t, probe)
	checkTerminalMetadata(t, probe)
	checkSelection(t, probe)
	checkScrollback(t, probe)
	checkProcessTracking(t, probe)
	checkKeymapDetails(t, probe)
	checkLocalAdministration(t, probe, true)
	checkInputEncoding(t, probe)
	checkBulkInput(t, probe)
	checkTheme(t, probe)
	checkListDir(t, probe)
	checkClipboard(t, probe)
	checkProgramStatus(t, probe)
	checkLayout(t, probe)
	checkLayoutEdges(t, probe)
	probe.conn.CloseNow()
	call("session.bootstrap", map[string]any{"event_scope": "all"})
	call("server.whoami", map[string]any{})
	call("server.tailscale.disable", map[string]any{})
	call("block_creator.list", map[string]any{})
	created := call("session.create", map[string]any{"label": "protocol probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sh"}, "cwd": root, "login": false}}}}}})
	sid := created["session_id"].(string)
	attached := call("session.attach", map[string]any{"session_id": sid})
	view := attached["view"].(map[string]any)
	w := view["focused_window"].(map[string]any)
	bid := w["focused_block_id"].(string)
	call("session.describe_block", map[string]any{"session_id": sid, "block_id": bid})
	call("client.keymap", map[string]any{})
	checkKeymap(t, call)
	ticket := call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
	dc, _, err := websocket.Dial(ctx, "ws://rex/api/data", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{"rex.data.terminal.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.CloseNow()
	secret, err := base64.RawURLEncoding.DecodeString(ticket["ticket"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := dc.Write(ctx, websocket.MessageBinary, append([]byte{1}, secret...)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, b, err := dc.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("data frame %d: %s", i, hex.EncodeToString(b))
		save("data-"+string(rune('0'+i))+".bin", b)
	}
	call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "paste", "payload": map[string]any{"text": "printf 'REX_PROTOCOL_PROBE\\n'\n"}}})
	time.Sleep(100 * time.Millisecond)
	call("com.superlogical.terminal.format", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"trim": true}})
	call("session.destroy", map[string]any{"session_id": sid})
	for _, method := range []string{"server.version", "session.list"} {
		if err := wsjson.Write(ctx, c, map[string]any{"type": "request", "id": method, "method": method, "payload": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		var response json.RawMessage
		if err := wsjson.Read(ctx, c, &response); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", method, response)
		save(strings.ReplaceAll(method, ".", "-")+".json", response)
	}
	discover := exec.CommandContext(ctx, *rexBinary, "--autostart=false", "--server", endpoint, "api", "list", "--json")
	discover.Env = env
	b, err := discover.CombinedOutput()
	if err != nil {
		t.Fatalf("api discovery: %v: %s", err, b)
	}
	if !json.Valid(b) {
		t.Fatalf("invalid API discovery JSON: %.1000s", b)
	}
	save("api.json", b)
	t.Logf("captured %d bytes of API schemas", len(b))
}
