package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func checkTheme(t *testing.T, c *testClient) {
	t.Helper()
	created := c.call("session.create", map[string]any{"label": "theme probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"].(string)
	connect := func() (*websocket.Conn, uint64) {
		ticket := c.call("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
		dc, welcome := c.data(ticket["ticket"].(string))
		return dc, binary.LittleEndian.Uint64(welcome[24:])
	}
	dc, nonce := connect()
	if _, _, err := dc.Read(c.ctx); err != nil {
		t.Fatal(err)
	}
	theme := map[string]any{"foreground": "#abcdef", "background": "#112233", "cursor": "#445566", "palette": []any{"1=#123456"}, "scheme": "light"}
	c.call("com.superlogical.terminal.set_theme", map[string]any{"session_id": sid, "block_id": bid, "args": theme})
	want := map[string]any{"type": "theme"}
	for k, v := range theme {
		want[k] = v
	}
	readTheme := func(dc *websocket.Conn, nonce uint64) {
		t.Helper()
		ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
		defer cancel()
		prefix := []byte(fmt.Sprintf("\x1b_rex1;%d;", nonce))
		var stream []byte
		for {
			_, chunk, err := dc.Read(ctx)
			if err != nil {
				t.Fatalf("theme marker missing: %v", err)
			}
			stream = append(stream, chunk...)
			for {
				i := bytes.Index(stream, prefix)
				if i < 0 {
					break
				}
				body := stream[i+len(prefix):]
				end := bytes.Index(body, []byte("\x1b\\"))
				if end < 0 {
					break
				}
				var marker map[string]any
				if err := json.Unmarshal(body[:end], &marker); err != nil {
					t.Fatal(err)
				}
				if marker["type"] == "theme" {
					if !reflect.DeepEqual(marker, want) {
						t.Fatalf("theme marker: got %v, want %v", marker, want)
					}
					return
				}
				stream = body[end+2:]
			}
		}
	}
	readTheme(dc, nonce)
	dc.CloseNow()
	_, failure := c.exchange("com.superlogical.terminal.set_theme", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"background": "invalid-color"}})
	if failure == nil {
		t.Fatal("invalid theme color was accepted")
	}
	t.Logf("invalid theme: %v", failure)
	// A fresh connection must retain the last valid theme.
	fresh, freshNonce := connect()
	readTheme(fresh, freshNonce)
}
