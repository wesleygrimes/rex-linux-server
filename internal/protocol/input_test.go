package protocol

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Verify terminal input using the bytes received by a raw PTY child.
// The child prints the bytes it received, so terminal echo cannot satisfy it.
func checkInputEncoding(t *testing.T, c *testClient) {
	t.Helper()
	program := `import os, tty
tty.setraw(0)
os.write(1, b"\x1b[?2004h\x1b[?1004h\x1b[?1003h\x1b[?1006hINPUT_READY\r\n")
data = b""
while True:
    data += os.read(0, 4096)
    os.write(1, b"BYTES:" + data.hex().encode() + b"\r\n")
`
	created := c.call("session.create", map[string]any{"label": "input encoding probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"].(string)
	args := func(a map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": a}
	}
	c.call("com.superlogical.terminal.resize", args(map[string]any{"columns": 240, "rows": 30, "cell_width_px": 8, "cell_height_px": 16}))
	waitFor := func(want string) {
		t.Helper()
		var screen string
		for i := 0; i < 100; i++ {
			screen = fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"])
			if strings.Contains(screen, want) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("PTY did not report %q; screen: %q", want, screen)
	}
	waitFor("INPUT_READY")
	tests := []struct {
		name, kind string
		payload    map[string]any
		bytes      string
	}{
		{"bracketed paste", "paste", map[string]any{"text": "hello\nworld"}, "\x1b[200~hello\nworld\x1b[201~"},
		{"focus in", "focus", map[string]any{"action": "in"}, "\x1b[I"},
		{"focus out", "focus", map[string]any{"action": "out"}, "\x1b[O"},
		{"mouse press", "mouse", map[string]any{"button": "left", "action": "press", "x": 3, "y": 4}, "\x1b[<0;4;5M"},
		{"mouse release", "mouse", map[string]any{"button": "left", "action": "release", "x": 3, "y": 4}, "\x1b[<0;4;5m"},
		{"wheel up", "wheel", map[string]any{"delta_y": -1, "delta_mode": "line", "x": 3, "y": 4}, "\x1b[<64;4;5M"},
		{"wheel right", "wheel", map[string]any{"delta_x": 1, "delta_mode": "line", "x": 3, "y": 4}, "\x1b[<67;4;5M"},
	}
	var received string
	for _, test := range tests {
		t.Logf("input case: %s", test.name)
		c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": test.kind, "payload": test.payload}})
		received += test.bytes
		waitFor("BYTES:" + hex.EncodeToString([]byte(received)))
	}
}

func checkBulkInput(t *testing.T, c *testClient) {
	t.Helper()
	const count = 512 << 10
	program := fmt.Sprintf(`import os, tty
tty.setraw(0)
os.write(1, b"BULK_READY\r\n")
n = 0
while n < %d:
    data = os.read(0, 4096)
    if n == 0:
        os.write(1, b"\x1b]0;bulk input probe\x07" + b"x" * 65536)
    n += len(data)
os.write(1, b"\r\nBULK_BYTES:" + str(n).encode() + b"\r\n")
while True:
    os.read(0, 4096)
`, count)
	created := c.call("session.create", map[string]any{"label": "bulk input probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"].(string)
	waitFor := func(want string) {
		t.Helper()
		for i := 0; i < 200; i++ {
			screen := fmt.Sprint(c.call("com.superlogical.terminal.format", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"trim": true}})["content"])
			if strings.Contains(screen, want) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("PTY did not report %s", want)
	}
	waitFor("BULK_READY")
	result := c.call("com.superlogical.terminal.write", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"data": strings.Repeat("a", count)}})
	if result["bytes_written"] != float64(count) {
		t.Fatalf("bulk input was not accepted: %v", result)
	}
	waitFor(fmt.Sprintf("BULK_BYTES:%d", count))
}
