package protocol

import (
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func checkClientLifecycle(t *testing.T, c *testClient) {
	t.Helper()
	c.call("session.attach_all", map[string]any{"event_scope": "all"})
	defer c.call("session.attach_all", map[string]any{"event_scope": "session"})
	target := dialCompatible(t, c.ctx, c.endpoint)
	defer target.conn.CloseNow()
	id := target.welcome["client"].(map[string]any)["id"]
	sid := c.call("session.create", map[string]any{"label": "client lifecycle"})["session_id"]
	c.call("session.attach", map[string]any{"session_id": sid})
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	for _, method := range []string{"session.view", "session.list_blocks", "session.destroy"} {
		if _, failure := target.exchange(method, map[string]any{"session_id": sid}); failure["code"] != "conflict" {
			t.Fatalf("unattached %s: %v", method, failure)
		}
	}
	target.call("session.attach", map[string]any{"session_id": sid})
	target.call("client.set_label", map[string]any{"label": "  remote client  "})
	info := c.call("client.inspect", map[string]any{"client_id": id})["client"].(map[string]any)
	if info["info"].(map[string]any)["label"] != "remote client" || len(info["session_ids"].([]any)) != 1 {
		t.Fatalf("client metadata: %v", info)
	}
	for _, a := range []map[string]any{{"label": strings.Repeat("x", 65)}, {"label": "bad\nlabel"}, {"client_id": id, "label": "another"}} {
		if _, failure := target.exchange("client.set_label", a); failure["code"] != "bad_request" {
			t.Fatalf("invalid client label: %v", failure)
		}
	}
	if got := c.call("client.list", map[string]any{"kinds": []any{"app"}})["clients"].([]any); len(got) != 0 {
		t.Fatalf("client kind filter: %v", got)
	}
	target.conn.CloseNow()
	var lost map[string]any
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		for _, value := range c.call("client.list", map[string]any{"session_id": sid})["clients"].([]any) {
			item := value.(map[string]any)
			if item["client_id"] == id && item["connection_state"] == "lost" {
				lost = item
				break
			}
		}
		if lost != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if lost == nil || lost["connection_lost_at"] == nil {
		t.Fatalf("disconnected client disappeared during reconnect grace: %v", lost)
	}
	conn, _, err := websocket.Dial(c.ctx, target.base+"/api/control", &websocket.DialOptions{HTTPClient: target.opts.HTTPClient, Subprotocols: []string{"rex.control.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := wsjson.Write(c.ctx, conn, map[string]any{"type": "hello", "reconnect_token": target.welcome["client"].(map[string]any)["reconnect_token"], "client": map[string]any{"kind": "cli", "name": "compatibility test"}}); err != nil {
		t.Fatal(err)
	}
	var welcome map[string]any
	if err := wsjson.Read(c.ctx, conn, &welcome); err != nil {
		t.Fatal(err)
	}
	resumed := welcome["client"].(map[string]any)
	if resumed["id"] != id || resumed["resumed"] != true {
		t.Fatalf("control reconnect lost identity: %v", welcome)
	}
	target.conn = conn
	target.call("session.detach", map[string]any{"session_id": sid})
	target.call("session.detach", map[string]any{"session_id": "session:00000000-0000-4000-8000-000000000000"})
	if len(c.call("client.list", map[string]any{"session_id": sid})["clients"].([]any)) != 1 {
		t.Fatal("detached client remained in session client list")
	}
	c.call("server.status", map[string]any{})
	joined, changed := false, false
	for _, event := range c.events {
		payload, _ := event["payload"].(map[string]any)
		item, _ := payload["client"].(map[string]any)
		if item["client_id"] == id {
			joined = joined || event["type"] == "client_joined"
			changed = changed || event["type"] == "client_changed"
		}
	}
	if !joined || !changed {
		t.Fatalf("client lifecycle events: joined=%v changed=%v", joined, changed)
	}
}
