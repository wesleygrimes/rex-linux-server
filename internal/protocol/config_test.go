package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket/wsjson"
)

// Call only against the isolated server started by this test suite.
func checkConfiguration(t *testing.T, c *testClient) {
	t.Helper()
	initial := c.call("client.keymap", map[string]any{})["config"].(map[string]any)
	path := initial["path"].(string)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Error(err)
		}
		c.call("server.config_reload", map[string]any{})
	}()
	modulePath := filepath.Join(filepath.Dir(path), "compatibility_helpers.lua")
	if err := os.WriteFile(modulePath, []byte("return {message='from module'}"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(modulePath)
	source := `local helper = require('compatibility_helpers')
rex.action{ name = 'echo_value', title = 'Echo', run = function(ctx, args)
  return { message = args.message, origin = ctx.origin }
end }
rex.mode('resize', {exclusive = true})
rex.bind('resize/left', 'pane.resize', {direction = 'left'})
local modes = rex.modes()
local bindings = rex.bindings()
rex.action{name='helpers',title='Helpers',run=function(ctx)
  return {message=helper.message, os=type(os), io=type(io), modes=modes, bindings=bindings,
    context_clean=ctx.action==nil and ctx.args==nil,
    layout=rex.layout.horizontal(.3,rex.layout.block{flavor='a'},rex.layout.block{flavor='b'})}
end}
rex.action{name='nested_echo',title='Nested echo',run=function() return rex.invoke('echo_value',{message='nested'}) end}
rex.action{name='queue_client',title='Queue client',run=function() rex.client.queue('test.client',{message='queued'}) end}
rex.host{ label = 'declared-host', endpoints = {'declared.example.ts.net'} }
rex.action{ name = 'remote_count', title = 'Remote count', run = function()
  local result, err = rex.server('remote').call('session.list', {})
  if not result then error(err) end
  return { count = #result.sessions }
end }
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded := c.call("server.config_reload", map[string]any{})
	if reloaded["generation"].(float64) <= initial["generation"].(float64) {
		t.Fatalf("config generation did not advance: %v", reloaded)
	}
	for _, value := range reloaded["diagnostics"].([]any) {
		if value.(map[string]any)["severity"] == "error" {
			t.Fatalf("config failed to load: %v", reloaded)
		}
	}
	action := c.call("client.action", map[string]any{"action": "echo_value", "args": map[string]any{"message": "hello"}})
	if action["result"].(map[string]any)["message"] != "hello" || action["result"].(map[string]any)["origin"] != "api" {
		t.Fatalf("config action context or arguments: %v", action)
	}
	keymap := c.call("client.keymap", map[string]any{"server_actions": "configured"})
	for _, value := range keymap["actions"].([]any) {
		record := value.(map[string]any)
		if record["kind"] != "lua" || record["category"] != "Actions" || strings.Contains(record["source"].(string), "/") {
			t.Fatalf("configured action metadata: %v", record)
		}
	}
	helper := c.call("client.action", map[string]any{"action": "helpers"})["result"].(map[string]any)
	if helper["message"] != "from module" || helper["os"] != "table" || helper["io"] != "table" || helper["context_clean"] != true {
		t.Fatalf("Lua config helpers: %v", helper)
	}
	if helper["layout"].(map[string]any)["split"].(map[string]any)["ratio"] != .3 {
		t.Fatalf("Lua layout helpers: %v", helper)
	}
	if nested := c.call("client.action", map[string]any{"action": "nested_echo"})["result"].(map[string]any); nested["message"] != "nested" {
		t.Fatalf("nested action: %v", nested)
	}
	queued := c.call("client.action", map[string]any{"action": "queue_client"})["client_actions"].([]any)
	if len(queued) != 1 || queued[0].(map[string]any)["action"] != "test.client" {
		t.Fatalf("queued client action: %v", queued)
	}
	if keymap["config"].(map[string]any)["hash"] == initial["hash"] {
		t.Fatal("config hash did not change")
	}
	modes, _ := keymap["modes"].([]any)
	if len(modes) != 1 || modes[0].(map[string]any)["name"] != "resize" || modes[0].(map[string]any)["exclusive"] != true {
		t.Fatalf("config mode declaration: %v", keymap)
	}
	hosts := c.call("hosts.list", map[string]any{})["hosts"].([]any)
	if len(hosts) != 1 || hosts[0].(map[string]any)["label"] != "declared-host" || hosts[0].(map[string]any)["source"] == nil {
		t.Fatalf("config host declaration: %v", hosts)
	}
	host := hosts[0].(map[string]any)
	hostID := host["id"]
	host["icon"] = "globe"
	stored := c.call("hosts.put", map[string]any{"host": host})["host"].(map[string]any)
	if stored["source"] != nil || stored["icon"] != "globe" {
		t.Fatalf("config host override: %v", stored)
	}
	c.call("hosts.remove", map[string]any{"id": hostID})
	c.call("server.config_reload", map[string]any{})
	if got := c.call("hosts.list", map[string]any{}); len(got["hosts"].([]any)) != 0 {
		t.Fatalf("removed config host reappeared after reload: %v", got)
	}
	if err := wsjson.Write(c.ctx, c.conn, map[string]any{"type": "request", "id": "remote-action", "method": "client.action", "payload": map[string]any{"action": "remote_count", "servers": []any{map[string]any{"name": "remote", "id": "remote-connection"}}}}); err != nil {
		t.Fatal(err)
	}
	seenAction, seenReply := false, false
	for !seenAction || !seenReply {
		var frame map[string]any
		if err := wsjson.Read(c.ctx, c.conn, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "event" {
			event := frame["payload"].(map[string]any)
			if event["type"] != "client_call_requested" {
				continue
			}
			call := event["payload"].(map[string]any)
			if call["server"] != "remote-connection" || call["method"] != "session.list" {
				t.Fatalf("remote call routing: %v", call)
			}
			if err := wsjson.Write(c.ctx, c.conn, map[string]any{"type": "request", "id": "remote-reply", "method": "client.reply", "payload": map[string]any{"call_id": call["call_id"], "result": map[string]any{"sessions": []any{map[string]any{"label": "remote"}}}}}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if frame["error"] != nil {
			t.Fatalf("remote action failed: %v", frame)
		}
		switch frame["id"] {
		case "remote-action":
			seenAction = true
			if frame["payload"].(map[string]any)["result"].(map[string]any)["count"] != float64(1) {
				t.Fatalf("remote action result: %v", frame)
			}
		case "remote-reply":
			seenReply = true
		default:
			t.Fatalf("unexpected response during remote action: %v", frame)
		}
	}
	if err := os.WriteFile(path, []byte("broken Lua !!"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := c.call("server.config_reload", map[string]any{})
	if len(failed["diagnostics"].([]any)) == 0 {
		t.Fatal("failed config reload did not report diagnostics")
	}
	if result := c.call("client.action", map[string]any{"action": "echo_value", "args": map[string]any{"message": "retained"}}); result["result"].(map[string]any)["message"] != "retained" {
		t.Fatal("failed config reload discarded previous actions")
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	c.call("server.config_reload", map[string]any{})
	before := c.call("client.keymap", map[string]any{})["config"].(map[string]any)["hash"]
	if err := os.WriteFile(modulePath, []byte("return {message='changed module'}"), 0600); err != nil {
		t.Fatal(err)
	}
	c.call("server.config_reload", map[string]any{})
	after := c.call("client.keymap", map[string]any{})["config"].(map[string]any)["hash"]
	if before == after {
		t.Fatal("editing a required module did not change the config hash")
	}
	if result := c.call("client.action", map[string]any{"action": "helpers"})["result"].(map[string]any); result["message"] != "changed module" {
		t.Fatalf("module cache survived reload: %v", result)
	}
}
