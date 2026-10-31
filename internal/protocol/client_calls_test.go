package protocol

import (
	"reflect"
	"testing"

	"github.com/coder/websocket/wsjson"
)

func checkClientCalls(t *testing.T, c *testClient) {
	t.Helper()
	c.call("session.attach_all", map[string]any{"event_scope": "all"})
	defer c.call("session.attach_all", map[string]any{"event_scope": "session"})
	target := dialCompatible(t, c.ctx, c.endpoint)
	defer target.conn.CloseNow()
	id := target.welcome["client"].(map[string]any)["id"].(string)
	actions := []any{map[string]any{"name": "test.echo", "kind": "client", "title": "Echo", "args": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}, "required": []any{"message"}, "additionalProperties": false}}}
	target.call("client.keymap", map[string]any{"actions": actions})
	inspected := c.call("client.inspect", map[string]any{"client_id": id})
	if !reflect.DeepEqual(inspected["actions"], actions) || inspected["client"].(map[string]any)["client_id"] != id {
		t.Fatalf("client inspection lost declared actions: %v", inspected)
	}
	if _, failure := c.exchange("client.perform", map[string]any{"client_id": id, "action": "unknown"}); failure == nil || failure["code"] != "not_supported" {
		t.Fatalf("undeclared action: %v", failure)
	}
	if _, failure := c.exchange("client.perform", map[string]any{"client_id": id, "action": "test.echo", "args": map[string]any{"message": 1}}); failure["code"] != "bad_request" {
		t.Fatalf("client action schema was not enforced: %v", failure)
	}
	type outcome struct {
		result, failure map[string]any
	}
	completed := make(chan outcome, 1)
	go func() {
		result, failure := c.exchange("client.perform", map[string]any{"client_id": id, "action": "test.echo", "args": map[string]any{"message": "hello"}, "timeout_ms": 1000})
		completed <- outcome{result, failure}
	}()
	var event map[string]any
	if err := wsjson.Read(c.ctx, target.conn, &event); err != nil {
		t.Fatal(err)
	}
	outer := event["payload"].(map[string]any)
	if event["type"] != "event" || outer["type"] != "client_perform_requested" {
		t.Fatalf("missing client action event: %v", event)
	}
	request := outer["payload"].(map[string]any)
	if request["action"] != "test.echo" || request["requester"].(map[string]any)["client_id"] != c.welcome["client"].(map[string]any)["id"] {
		t.Fatalf("client request identity: %v", request)
	}
	attacker := dialCompatible(t, c.ctx, c.endpoint)
	if _, failure := attacker.exchange("client.reply", map[string]any{"call_id": request["call_id"], "result": nil}); failure == nil || failure["code"] != "not_found" {
		t.Fatalf("another client answered a private call: %v", failure)
	}
	attacker.conn.CloseNow()
	target.call("client.reply", map[string]any{"call_id": request["call_id"], "result": map[string]any{"echo": "hello"}})
	got := <-completed
	if got.failure != nil || got.result["performed_by"] != id || got.result["result"].(map[string]any)["echo"] != "hello" {
		t.Fatalf("client action outcome: %+v", got)
	}
	if _, failure := target.exchange("client.reply", map[string]any{"call_id": request["call_id"], "result": nil}); failure == nil || failure["code"] != "not_found" {
		t.Fatalf("repeated reply accepted: %v", failure)
	}
	if _, failure := c.exchange("client.perform", map[string]any{"client_id": id, "action": "test.echo", "args": map[string]any{"message": "timeout"}, "timeout_ms": 1}); failure == nil || failure["code"] != "timeout" {
		t.Fatalf("unanswered action did not time out: %v", failure)
	}
	c.call("server.status", map[string]any{})
	outcomes := map[string]bool{}
	for _, event := range c.events {
		if event["type"] != "client_performed" {
			continue
		}
		report := event["payload"].(map[string]any)
		if report["client_id"] == id {
			outcomes[report["outcome"].(string)] = true
		}
	}
	if !outcomes["performed"] || !outcomes["refused"] || !outcomes["timeout"] {
		t.Fatalf("client action outcome events: %v", outcomes)
	}
}
