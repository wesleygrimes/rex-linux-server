package protocol

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

func checkProgramStatus(t *testing.T, c *testClient) {
	t.Helper()
	program := `import os, tty
tty.setraw(0)
os.write(1, b"STATUS_READY\r\n")
reports = {
    b"a": b"state=working:id=build:progress=25:app=demo:title=QnVpbGQ=:msg=V29ya2luZw==",
    b"b": b"state=blocked:id=build:kind=permission:msg=QXBwcm92ZT8=",
    b"c": b"state=done:id=build/test:msg=UGFzc2Vk",
    b"d": b"state=clear:id=build",
    b"e": b"state=working:app=demo",
    b"f": b"state=error:id=error:app=demo",
    b"g": b"state=working:id=build/test",
    b"h": b"state=done:id=build:app=parent",
}
while True:
    command = os.read(0, 1)
    if command == b"p":
        os.write(1, b"\x1b]133;A\x07")
    elif command == b"m":
        for i in range(65):
            os.write(1, ("\x1b]7501;state=idle:id=n%03d\x1b\\" % i).encode())
    else:
        os.write(1, b"\x1b]7501;" + reports[command] + b"\x1b\\")
    os.write(1, b"STATUS_SENT:" + command + b"\r\n")
`
	created := c.call("session.create", map[string]any{"label": "program status probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"].(string)
	args := func(a map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": a}
	}
	waitFor := func(want string) {
		for i := 0; i < 100; i++ {
			screen := fmt.Sprint(c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"])
			if strings.Contains(screen, want) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("status fixture did not report " + want)
	}
	waitFor("STATUS_READY")
	child := c.call("com.superlogical.terminal.process", args(map[string]any{}))["child"].(map[string]any)
	normalize := func(record map[string]any) map[string]any {
		t.Helper()
		if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(record["updated_at"])); err != nil {
			t.Fatalf("invalid status timestamp: %v", record)
		}
		owner, _ := record["owner"].(map[string]any)
		if owner["pid"] != child["pid"] || owner["name"] == nil || owner["name"] == "" {
			t.Fatalf("status was not attributed to its PTY child: %v", record)
		}
		out := maps.Clone(record)
		delete(out, "updated_at")
		delete(out, "owner")
		return out
	}
	read := func() []map[string]any {
		out := c.call("com.superlogical.terminal.program_status", args(map[string]any{}))
		records := []map[string]any{}
		for _, record := range out["records"].([]any) {
			records = append(records, normalize(record.(map[string]any)))
		}
		return records
	}
	working := map[string]any{"id": "build", "state": "working", "progress": float64(25), "app": "demo", "effective_app": "demo", "title": "Build", "msg": "Working"}
	blocked := map[string]any{"id": "build", "state": "blocked", "kind": "permission", "msg": "Approve?"}
	done := map[string]any{"id": "build/test", "state": "done", "msg": "Passed"}
	root := map[string]any{"state": "working", "app": "demo", "effective_app": "demo"}
	inherited := map[string]any{"id": "build/test", "state": "working", "effective_app": "demo"}
	parent := map[string]any{"id": "build", "state": "done", "app": "parent", "effective_app": "parent"}
	reparented := map[string]any{"id": "build/test", "state": "working", "effective_app": "parent"}
	failed := map[string]any{"id": "error", "state": "error", "app": "demo", "effective_app": "demo"}
	for _, test := range []struct {
		command string
		want    []map[string]any
		changed map[string]any
		removed []any
		reason  string
	}{
		{"a", []map[string]any{working}, working, nil, ""},
		{"b", []map[string]any{blocked}, blocked, nil, ""},
		{"c", []map[string]any{blocked, done}, done, nil, ""},
		{"d", []map[string]any{}, nil, []any{"build", "build/test"}, "clear"},
		{"e", []map[string]any{root}, root, nil, ""},
		{"g", []map[string]any{root, inherited}, inherited, nil, ""},
		{"h", []map[string]any{root, parent, reparented}, parent, nil, ""},
		{"f", []map[string]any{root, parent, reparented, failed}, failed, nil, ""},
		{"p", []map[string]any{parent, failed}, nil, []any{"", "build/test"}, "prompt"},
	} {
		c.events = nil
		c.call("com.superlogical.terminal.write", args(map[string]any{"data": test.command}))
		waitFor("STATUS_SENT:" + test.command)
		if records := read(); !reflect.DeepEqual(records, test.want) {
			t.Fatalf("program status %s: got %v want %v", test.command, records, test.want)
		}
		found := false
		for _, event := range c.events {
			if event["type"] != "session_event" {
				continue
			}
			inner := event["payload"].(map[string]any)["event"].(map[string]any)
			if inner["type"] != "block_event" {
				continue
			}
			block := inner["payload"].(map[string]any)
			if block["block_id"] != bid {
				continue
			}
			payload, _ := block["payload"].(map[string]any)
			if block["name"] == "program_status_changed" && test.changed != nil {
				if payload["reason"] != "report" || !reflect.DeepEqual(normalize(payload["record"].(map[string]any)), test.changed) {
					t.Fatalf("program status change event: %v", payload)
				}
				found = true
			}
			if block["name"] == "program_status_removed" && test.removed != nil {
				if payload["reason"] != test.reason || !reflect.DeepEqual(payload["ids"], test.removed) {
					t.Fatalf("program status removal event: %v", payload)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("status event missing for " + test.command)
		}
	}
	c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "focus", "payload": map[string]any{"action": "in"}}})
	if records := read(); !reflect.DeepEqual(records, []map[string]any{parent, failed}) {
		t.Fatalf("focus changed retained statuses: %v", records)
	}
	c.call("com.superlogical.terminal.reset", args(map[string]any{}))
	if records := read(); len(records) != 0 {
		t.Fatalf("reset did not clear statuses: %v", records)
	}
	c.call("com.superlogical.terminal.write", args(map[string]any{"data": "m"}))
	waitFor("STATUS_SENT:m")
	records := read()
	if len(records) != 64 || records[0]["id"] != "n001" || records[63]["id"] != "n064" {
		t.Fatalf("status capacity or ordering differs: %v", records)
	}
}
