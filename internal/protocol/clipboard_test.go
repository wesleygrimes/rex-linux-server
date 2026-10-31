package protocol

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func checkClipboard(t *testing.T, c *testClient) {
	t.Helper()
	program := `import base64, os, tty
tty.setraw(0)
os.write(1, b"CLIPBOARD_READY\r\n")
while True:
    command = os.read(0, 1)
    text = {b"s": b"clip text", b"l": b"x" * 100000, b"u": "A🙂B\n".encode(), b"e": b""}[command]
    os.write(1, b"\x1b]52;c;" + base64.b64encode(text) + b"\x07CLIPBOARD_COMMAND:" + command + b"\r\n")
`
	created := c.call("session.create", map[string]any{"label": "clipboard probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"].(string)
	args := func(a map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": a}
	}
	for i := 0; i < 100; i++ {
		screen := fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"])
		if strings.Contains(screen, "CLIPBOARD_READY") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, failure := c.exchange("com.superlogical.terminal.clipboard_read", args(map[string]any{}))
	if failure["status"] != float64(404) {
		t.Fatalf("empty clipboard: result=%v error=%v", out, failure)
	}
	for index, test := range []struct{ command, text string }{{"s", "clip text"}, {"l", strings.Repeat("x", 100000)}, {"u", "A🙂B\n"}, {"s", "clip text"}, {"s", "clip text"}} {
		t.Logf("clipboard case %s", test.command)
		c.events = nil
		c.call("com.superlogical.terminal.write", args(map[string]any{"data": test.command}))
		var written map[string]any
		for i := 0; i < 100 && written == nil; i++ {
			c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))
			for _, event := range c.events {
				if event["type"] != "session_event" {
					continue
				}
				inner := event["payload"].(map[string]any)["event"].(map[string]any)
				if inner["type"] == "block_event" {
					block := inner["payload"].(map[string]any)
					if block["block_id"] == bid && block["name"] == "clipboard_written" {
						written = block["payload"].(map[string]any)
					}
				}
			}
			if written == nil {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if written == nil {
			t.Fatalf("clipboard_written event missing for %s", test.command)
		}
		wantEvent := map[string]any{"id": float64(index + 1)}
		if len(test.text) > 0 {
			wantEvent["bytes"] = float64(len(test.text))
			if test.command != "l" {
				wantEvent["text"] = test.text
			}
		}
		if !reflect.DeepEqual(written, wantEvent) {
			t.Fatalf("clipboard event: got %v want %v", written, wantEvent)
		}
		// Read the complete transfer through several chunks, checking each offset.
		var received string
		for {
			out := c.call("com.superlogical.terminal.clipboard_read", args(map[string]any{"offset": len(received), "max_bytes": 8192}))
			chunk := out["data"].(string)
			received += chunk
			if out["id"] != wantEvent["id"] || out["total"] != float64(len(test.text)) {
				t.Fatalf("clipboard chunk metadata: %v", out)
			}
			if out["eof"] == true {
				break
			}
			if chunk == "" {
				t.Fatal("clipboard chunk did not make progress")
			}
		}
		if received != test.text {
			t.Fatal("clipboard chunks changed the transfer content")
		}
		if test.command == "u" {
			out := c.call("com.superlogical.terminal.clipboard_read", args(map[string]any{"offset": 1, "max_bytes": 1}))
			if out["data"] != "🙂" || out["eof"] != false {
				t.Fatalf("UTF-8 clipboard boundary: %v", out)
			}
		}
		for _, negative := range []struct {
			args   map[string]any
			status float64
		}{{map[string]any{"id": 99999}, 404}, {map[string]any{"offset": -1}, 400}, {map[string]any{"max_bytes": -1}, 400}} {
			_, failure := c.exchange("com.superlogical.terminal.clipboard_read", args(negative.args))
			if failure["status"] != negative.status {
				t.Fatalf("clipboard invalid arguments %v: %v", negative.args, failure)
			}
		}
	}
	_, failure = c.exchange("com.superlogical.terminal.clipboard_read", args(map[string]any{"id": 1}))
	if failure["status"] != float64(404) {
		t.Fatalf("clipboard history did not evict the fifth-oldest write: %v", failure)
	}
	c.call("com.superlogical.terminal.write", args(map[string]any{"data": "e"}))
	for i := 0; i < 100; i++ {
		screen := fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"])
		if strings.Contains(screen, "CLIPBOARD_COMMAND:e") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An empty legacy OSC 52 write does not add a clipboard entry.
	out = c.call("com.superlogical.terminal.clipboard_read", args(map[string]any{}))
	if out["id"] != float64(5) || out["data"] != "clip text" {
		t.Fatalf("empty OSC 52 changed the stored clipboard: %v", out)
	}
}
