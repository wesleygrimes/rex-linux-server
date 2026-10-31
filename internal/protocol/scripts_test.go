package protocol

import (
	"reflect"
	"testing"
)

func checkClientScripts(t *testing.T, c *testClient) {
	t.Helper()
	created := c.call("session.create", map[string]any{"label": "script keys", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}}}}}})
	sid := created["session_id"].(string)
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	defer c.call("client.set_script", map[string]any{"scripts": []any{}})
	loaded := c.call("client.set_script", map[string]any{"scripts": []any{map[string]any{"name": "keys.lua", "source": "rex.bind('ctrl+n', function() return true end)\nrex.bind('ctrl+b>x', function() return true end)"}}})
	if !reflect.DeepEqual(loaded["bindings"], []any{"ctrl+n"}) {
		t.Fatalf("script key interests: %v", loaded)
	}
	keymap := c.call("client.keymap", map[string]any{"server_actions": "none"})
	bindings, _ := keymap["bindings"].([]any)
	if len(bindings) != 2 || bindings[0].(map[string]any)["action"] != "client.key.forward" {
		t.Fatalf("script bindings missing from keymap: %v", keymap)
	}
	key := func(name, text, mods string, point int, prefix []any) map[string]any {
		input := map[string]any{"session_id": sid, "block_id": bid, "key": map[string]any{"key": name, "text": text, "mods": mods, "consumed_mods": "", "unshifted_codepoint": point, "composing": false, "action": "press"}}
		if prefix != nil {
			input["prefix"] = prefix
		}
		return c.call("client.key", input)
	}
	if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != true {
		t.Fatalf("script did not consume bound key: %v", got)
	}
	if got := key("key_x", "x", "", 'x', []any{"ctrl+b"}); got["handled"] != true {
		t.Fatalf("script did not consume key sequence: %v", got)
	}
	if _, failure := c.exchange("client.set_script", map[string]any{"scripts": []any{map[string]any{"name": "broken.lua", "source": "invalid lua !!"}}}); failure == nil {
		t.Fatal("invalid Lua replaced the running scripts")
	}
	if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != true {
		t.Fatal("failed script replacement discarded the old VM")
	}
	for _, value := range []string{"true", "false", "nil", "1"} {
		c.call("client.set_script", map[string]any{"scripts": []any{map[string]any{"source": "assert(os==nil and io==nil and require==nil)\nrex.bind('ctrl+n',function() return " + value + " end)"}}})
		if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != (value == "true") {
			t.Fatalf("Lua return %s consumed key incorrectly: %v", value, got)
		}
	}
	c.call("client.set_script", map[string]any{"scripts": []any{map[string]any{"source": "rex.bind('ctrl+n',function() rex.bind('ctrl+x',function() return true end);rex.unbind('ctrl+n');return true end)"}}})
	if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != true {
		t.Fatalf("initial handler: %v", got)
	}
	if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != false {
		t.Fatalf("late unbind did not remove handler: %v", got)
	}
	if got := key("key_x", "\x18", "ctrl", 'x', nil); got["handled"] != false {
		t.Fatalf("late binding expanded the fixed key interests: %v", got)
	}
	if _, failure := c.exchange("client.key", map[string]any{"session_id": sid, "block_id": bid, "prefix": []any{"ctrl+"}, "key": map[string]any{"key": "key_x", "action": "press"}}); failure["code"] != "bad_request" {
		t.Fatalf("invalid prefix accepted: %v", failure)
	}
	diagnosed := c.call("client.set_script", map[string]any{"scripts": []any{map[string]any{"source": "rex.bind('ctrl+notakey',function() return true end)\nrex.bind('ctrl+n',function() return true end)"}}})
	if warnings, _ := diagnosed["diagnostics"].([]any); len(warnings) == 0 {
		t.Fatalf("invalid Lua binding had no diagnostic: %v", diagnosed)
	}
	if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != true {
		t.Fatalf("invalid binding discarded valid bindings: %v", got)
	}
	c.call("client.set_script", map[string]any{"scripts": []any{}})
	if got := key("key_n", "\x0e", "ctrl", 'n', nil); got["handled"] != false {
		t.Fatal("unloaded script still consumed a key")
	}
}
