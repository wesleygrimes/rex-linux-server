package protocol

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/coder/websocket"
	"strings"
	"testing"
	"time"
)

func checkTerminalMutations(t *testing.T, c *testClient, path string) {
	t.Helper()
	created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []any{path, "terminal"}, "login": false}}}}}})
	sid := created["session_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	args := func(a map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": a}
	}
	screen := func() string {
		return c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"].(string)
	}
	wait := func(text string) {
		t.Helper()
		for deadline := time.Now().Add(time.Second); ; {
			if strings.Contains(screen(), text) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("terminal missing %q", text)
			}
			time.Sleep(time.Millisecond)
		}
	}
	write := func(data string) { c.call("com.superlogical.terminal.write", args(map[string]any{"data": data})) }
	wait("INFERENCE_READY")
	ticket := c.call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
	data, _ := c.data(ticket["ticket"].(string))
	defer data.CloseNow()
	if _, snapshot, err := data.Read(c.ctx); err != nil || !bytes.HasPrefix(snapshot, []byte("GHOSTSNP")) {
		t.Fatalf("initial snapshot: %v", err)
	}
	write("u")
	wait("UNFINISHED_BEFORE")
	c.call("com.superlogical.terminal.resize", args(map[string]any{"columns": 100, "rows": 30}))
	c.call("com.superlogical.terminal.set_theme", args(map[string]any{"foreground": "#aabbcc"}))
	c.call("com.superlogical.terminal.clear", args(map[string]any{}))
	if !strings.Contains(screen(), "UNFINISHED_BEFORE") {
		t.Fatal("clear interrupted the child's unfinished escape sequence")
	}
	write("f")
	wait("FINISHED_AFTER")
	if strings.Contains(screen(), "UNFINISHED_BEFORE") {
		t.Fatal("deferred clear was not applied at the parser boundary")
	}
	write("u")
	wait("UNFINISHED_BEFORE")
	c.call("com.superlogical.terminal.reset", args(map[string]any{}))
	time.Sleep(300 * time.Millisecond)
	write("g")
	wait("FORCED_AFTER")
	if strings.Contains(screen(), "UNFINISHED_BEFORE") {
		t.Fatal("forced reset retained stale screen state")
	}
	readCtx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	for {
		_, _, err := data.Read(readCtx)
		if err != nil {
			if readCtx.Err() != nil {
				t.Fatal("forced parser recovery did not invalidate the data stream")
			}
			break
		}
	}
	ticket = c.call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
	fresh, _ := c.data(ticket["ticket"].(string))
	defer fresh.CloseNow()
	if _, snapshot, err := fresh.Read(c.ctx); err != nil || !bytes.HasPrefix(snapshot, []byte("GHOSTSNP")) {
		t.Fatalf("recovery snapshot: %v", err)
	}
	write("u")
	wait("UNFINISHED_BEFORE")
	ticket = c.call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})

	partial, _, err := websocket.Dial(c.ctx, c.base+"/api/data", c.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer partial.CloseNow()
	token, err := base64.RawURLEncoding.DecodeString(ticket["ticket"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.Write(c.ctx, websocket.MessageBinary, append([]byte{1}, token...)); err != nil {
		t.Fatal(err)
	}
	_, rejected, err := partial.Read(c.ctx)
	if err != nil || !bytes.Equal(rejected, []byte{0x82, 2}) {
		t.Fatalf("unfinished parser snapshot rejection: %x %v", rejected, err)
	}
}
