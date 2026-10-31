package protocol

import (
	"strings"
	"testing"
	"time"
)

func checkSelection(t *testing.T, c *testClient) {
	t.Helper()
	program := "import os,time\nos.write(1,b'HELLO world\\r\\n\\x1b]8;;https://example.com/link\\x1b\\\\LINK\\x1b]8;;\\x1b\\\\\\r\\n')\ntime.sleep(60)"
	created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []any{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"]
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	for deadline := time.Now().Add(time.Second); ; {
		content := c.call("com.superlogical.terminal.format", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"trim": true}})["content"].(string)
		if strings.Contains(content, "HELLO world") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selection text not rendered")
		}
		time.Sleep(time.Millisecond)
	}
	c.events = nil
	for _, mouse := range []map[string]any{{"action": "press", "x": 0}, {"action": "motion", "x": 4}, {"action": "release", "x": 4}} {
		mouse["button"], mouse["y"] = "left", 0
		c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "mouse", "payload": mouse}})
	}
	c.call("server.status", map[string]any{})
	for _, outer := range c.events {
		if outer["type"] != "session_event" {
			continue
		}
		event := outer["payload"].(map[string]any)["event"].(map[string]any)
		if event["type"] != "block_event" {
			continue
		}
		payload := event["payload"].(map[string]any)
		if payload["block_id"] == bid && payload["name"] == "selection_copied" {
			if got := payload["payload"].(map[string]any)["text"]; got != "HELLO" {
				t.Fatalf("selection copied %q", got)
			}
			checkHyperlink(t, c, sid, bid)
			return
		}
	}
	t.Fatal("drag selection did not produce selection_copied")
}

func checkHyperlink(t *testing.T, c *testClient, sid, bid any) {
	t.Helper()
	c.events = nil
	for _, action := range []string{"press", "release"} {
		c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "mouse", "payload": map[string]any{"action": action, "button": "left", "x": 0, "y": 1}}})
	}
	for deadline := time.Now().Add(time.Second); ; {
		c.call("server.status", map[string]any{})
		for _, outer := range c.events {
			if outer["type"] != "session_event" {
				continue
			}
			event := outer["payload"].(map[string]any)["event"].(map[string]any)
			if event["type"] != "block_event" {
				continue
			}
			payload := event["payload"].(map[string]any)
			if payload["block_id"] == bid && payload["name"] == "url_clicked" {
				if payload["payload"].(map[string]any)["url"] != "https://example.com/link" {
					t.Fatalf("clicked hyperlink: %v", payload)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("click did not report the OSC 8 hyperlink")
		}
		time.Sleep(time.Millisecond)
	}
}

func checkScrollback(t *testing.T, c *testClient) {
	t.Helper()
	program := "import os,time\nos.write(1,b''.join(('ROW%03d\\r\\n'%i).encode() for i in range(20))+b'SCROLL_READY\\r\\n')\ntime.sleep(60)"
	created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []any{"/usr/bin/python3", "-u", "-c", program}, "login": false, "size": map[string]any{"columns": 80, "rows": 10}}}}}}})
	sid := created["session_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	for deadline := time.Now().Add(time.Second); ; {
		content := c.call("com.superlogical.terminal.format", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"trim": true}})["content"].(string)
		if strings.Contains(content, "SCROLL_READY") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scrollback fixture did not start")
		}
		time.Sleep(time.Millisecond)
	}
	c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "wheel", "payload": map[string]any{"delta_y": -5, "delta_mode": "line", "x": 0, "y": 0}}})
	c.events = nil
	for _, mouse := range []map[string]any{{"action": "press", "x": 0}, {"action": "motion", "x": 5}, {"action": "release", "x": 5}} {
		mouse["button"], mouse["y"] = "left", 0
		c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "mouse", "payload": mouse}})
	}
	for deadline := time.Now().Add(time.Second); ; {
		c.call("server.status", map[string]any{})
		for _, outer := range c.events {
			if outer["type"] != "session_event" {
				continue
			}
			event := outer["payload"].(map[string]any)["event"].(map[string]any)
			if event["type"] != "block_event" {
				continue
			}
			payload := event["payload"].(map[string]any)
			if payload["block_id"] == bid && payload["name"] == "selection_copied" {
				if text := payload["payload"].(map[string]any)["text"]; text != "ROW007" {
					t.Fatalf("scrolled viewport selection: %q", text)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("scrolled selection produced no copy")
		}
		time.Sleep(time.Millisecond)
	}
}
