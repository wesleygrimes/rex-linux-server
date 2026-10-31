package protocol

import (
	"testing"
	"time"
)

func checkTerminalOwnership(t *testing.T, c *testClient) {
	t.Helper()
	created := c.call("session.create", map[string]any{"label": "terminal ownership", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}}}}}})
	sid := created["session_id"].(string)
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	other := dialCompatible(t, c.ctx, c.endpoint)
	defer other.conn.CloseNow()
	other.call("session.attach", map[string]any{"session_id": sid})
	firstID := c.welcome["client"].(map[string]any)["id"]
	secondID := other.welcome["client"].(map[string]any)["id"]
	resize := func(client *testClient, columns int, mode string) {
		client.call("com.superlogical.terminal.resize", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"columns": columns, "rows": 30, "mode": mode}})
	}
	check := func(columns int, owner any, count int) {
		t.Helper()
		size := c.call("com.superlogical.terminal.size", map[string]any{"session_id": sid, "block_id": bid})
		if size["columns"] != float64(columns) || size["owner"] != owner || len(size["desired"].([]any)) != count {
			t.Fatalf("terminal size/owner/desires: %v; want %d %v %d", size, columns, owner, count)
		}
	}
	resize(c, 100, "take")
	check(100, firstID, 0)
	resize(other, 120, "advisory")
	check(100, firstID, 1)
	resize(other, 125, "take")
	check(125, secondID, 1)
	resize(c, 110, "advisory")
	check(125, secondID, 2)
	resize(other, 0, "release")
	check(110, firstID, 1)
	resize(other, 130, "take")
	other.call("session.detach", map[string]any{"session_id": sid})
	check(110, firstID, 1)
	other.call("session.attach", map[string]any{"session_id": sid})
	resize(other, 140, "take")
	other.conn.CloseNow()
	for deadline := time.Now().Add(time.Second); ; {
		size := c.call("com.superlogical.terminal.size", map[string]any{"session_id": sid, "block_id": bid})
		if size["owner"] == firstID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected owner was not promoted: %v", size)
		}
		time.Sleep(time.Millisecond)
	}
	check(110, firstID, 1)
}
