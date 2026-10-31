package protocol

import (
	"testing"
	"time"
)

func checkProcessTracking(t *testing.T, c *testClient) {
	t.Helper()
	created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []any{"/bin/sh", "-i"}, "login": false}}}}}})
	sid := created["session_id"]
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	args := map[string]any{"session_id": sid, "block_id": bid}
	initial := c.call("com.superlogical.terminal.process", args)
	child, ok := initial["child"].(map[string]any)
	if !ok || child["pid"] == nil || child["name"] == "" {
		t.Fatalf("terminal child identity: %v", initial)
	}
	args["args"] = map[string]any{"data": "sleep 5\n"}
	c.call("com.superlogical.terminal.write", args)
	delete(args, "args")
	var foreground map[string]any
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		result := c.call("com.superlogical.terminal.process", args)
		foreground, _ = result["foreground"].(map[string]any)
		if foreground["name"] == "sleep" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if foreground["name"] != "sleep" || foreground["pid"] == child["pid"] {
		t.Fatalf("foreground child was not tracked: %v", foreground)
	}
	for deadline := time.Now().Add(time.Second); ; {
		c.call("server.status", map[string]any{})
		found := false
		for _, outer := range c.events {
			if outer["type"] != "session_event" {
				continue
			}
			event := outer["payload"].(map[string]any)["event"].(map[string]any)
			if event["type"] != "block_event" {
				continue
			}
			payload := event["payload"].(map[string]any)
			if payload["block_id"] != bid || payload["name"] != "process_changed" {
				continue
			}
			process := payload["payload"].(map[string]any)
			info, _ := process["foreground"].(map[string]any)
			found = found || info["name"] == "sleep"
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no foreground process_changed event")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
