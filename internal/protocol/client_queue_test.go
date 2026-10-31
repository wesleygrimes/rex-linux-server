package protocol

import (
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
)

func checkClientQueue(t *testing.T, c *testClient) {
	t.Helper()
	target := dialCompatible(t, c.ctx, c.endpoint)
	defer target.conn.CloseNow()
	targetID := target.welcome["client"].(map[string]any)["id"]
	send := func(timeout int) string {
		c.n++
		id := fmt.Sprint(c.n)
		if err := wsjson.Write(c.ctx, c.conn, map[string]any{"type": "request", "id": id, "method": "client.perform", "payload": map[string]any{"client_id": targetID, "action": "queue.probe", "timeout_ms": timeout}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	read := func(conn *testClient) map[string]any {
		var frame map[string]any
		if err := wsjson.Read(c.ctx, conn.conn, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	nextPerform := func() map[string]any {
		for {
			frame := read(target)
			if frame["type"] != "event" {
				t.Fatalf("unexpected target frame: %v", frame)
			}
			event := frame["payload"].(map[string]any)
			if event["type"] == "client_perform_requested" {
				return event["payload"].(map[string]any)
			}
		}
	}
	nextResponse := func() map[string]any {
		for {
			frame := read(c)
			if frame["type"] == "event" {
				c.events = append(c.events, frame["payload"].(map[string]any))
				continue
			}
			return frame
		}
	}
	firstID := send(1000)
	first := nextPerform()
	secondID := send(100)
	// The second request's timeout starts when the first has completed.
	time.Sleep(150 * time.Millisecond)
	if err := wsjson.Write(c.ctx, target.conn, map[string]any{"type": "request", "id": "reply-first", "method": "client.reply", "payload": map[string]any{"call_id": first["call_id"], "result": nil}}); err != nil {
		t.Fatal(err)
	}
	var second map[string]any
	replied := false
	for second == nil || !replied {
		frame := read(target)
		if frame["type"] == "response" {
			if frame["id"] != "reply-first" || frame["error"] != nil {
				t.Fatalf("first reply: %v", frame)
			}
			replied = true
			continue
		}
		outer := frame["payload"].(map[string]any)
		if outer["type"] == "client_perform_requested" {
			second = outer["payload"].(map[string]any)
		}
	}
	target.call("client.reply", map[string]any{"call_id": second["call_id"], "error": map[string]any{"code": "refused", "message": "test refusal"}})
	seen := map[string]map[string]any{}
	for len(seen) < 2 {
		frame := nextResponse()
		seen[fmt.Sprint(frame["id"])] = frame
	}
	if seen[firstID]["error"] != nil {
		t.Fatalf("first queued call: %v", seen[firstID])
	}
	failure, _ := seen[secondID]["error"].(map[string]any)
	if failure["code"] != "refused" {
		t.Fatalf("queued timeout or refusal changed: %v", seen[secondID])
	}
	activeID := send(3000)
	nextPerform()
	expected := map[string]bool{activeID: true}
	for i := 0; i < 17; i++ {
		expected[send(3000)] = true
	}
	full := nextResponse()
	failure, _ = full["error"].(map[string]any)
	if failure["code"] != "client_unavailable" {
		t.Fatalf("full client queue: %v", full)
	}
	delete(expected, fmt.Sprint(full["id"]))
	target.conn.CloseNow()
	for len(expected) > 0 {
		frame := nextResponse()
		id := fmt.Sprint(frame["id"])
		if !expected[id] {
			t.Fatalf("unexpected queued response: %v", frame)
		}
		failure, _ := frame["error"].(map[string]any)
		if failure["code"] != "client_unavailable" {
			t.Fatalf("disconnected client queue: %v", frame)
		}
		delete(expected, id)
	}
}
