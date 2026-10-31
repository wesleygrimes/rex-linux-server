package protocol

import "testing"

func checkKeymapDetails(t *testing.T, c *testClient) {
	t.Helper()
	bind := func(keys []any, action string) map[string]any { return map[string]any{"keys": keys, "action": action} }
	layer := func(name string, bindings, removals []any) map[string]any {
		return map[string]any{"name": name, "bindings": bindings, "removals": removals}
	}
	for _, item := range []struct{ input, want string }{{"shift+ctrl+N", "ctrl+shift+n"}, {"cmd+shift+n", "shift+cmd+n"}, {"Control+N", "ctrl+n"}, {"ctrl+[keyn]", "ctrl+[KeyN]"}, {"ctrl+ ", "ctrl+space"}, {"super+n", "cmd+n"}, {"ctrl+notakey", "ctrl+notakey"}, {"ctrl+é", "ctrl+é"}} {
		result := c.call("client.keymap", map[string]any{"server_actions": "none", "overrides": []any{layer("settings", []any{bind([]any{item.input}, "foo")}, nil)}})
		keys := result["bindings"].([]any)[0].(map[string]any)["keys"].([]any)
		if len(keys) != 1 || keys[0] != item.want {
			t.Fatalf("key %q canonicalized as %v; want %q", item.input, keys, item.want)
		}
	}
	defaults := []any{layer("client defaults", []any{bind([]any{"cmd+b"}, "foo")}, []any{bind([]any{"cmd+z"}, "stale")})}
	result := c.call("client.keymap", map[string]any{"actions": []any{map[string]any{"name": "known", "kind": "client"}}, "defaults": defaults, "overrides": []any{layer("settings", []any{bind([]any{"cmd+b", "c"}, "foo")}, []any{bind([]any{"cmd+y"}, "stale")})}})
	shadow, stale, unknown := false, false, false
	for _, value := range result["diagnostics"].([]any) {
		d := value.(map[string]any)
		if d["source"] == "client defaults" {
			if d["key"] != "cmd+b" {
				t.Fatalf("default diagnostic should only report shadowing: %v", d)
			}
			shadow = true
		}
		stale = stale || d["source"] == "settings" && d["key"] == "cmd+y" && d["action"] == "stale"
		unknown = unknown || d["source"] == "settings" && d["key"] == "cmd+b c" && d["action"] == "foo"
	}
	if !shadow || !stale || !unknown {
		t.Fatalf("keymap diagnostics shadow=%v stale=%v unknown=%v: %v", shadow, stale, unknown, result["diagnostics"])
	}
	modeBinding := bind([]any{"left"}, "foo")
	modeBinding["mode"] = "resize"
	blocked := map[string]any{"keys": []any{"left"}, "mode": "resize"}
	result = c.call("client.keymap", map[string]any{"defaults": []any{layer("defaults", []any{modeBinding}, nil)}, "overrides": []any{layer("settings", nil, []any{blocked})}})
	modes := result["modes"].([]any)
	if len(modes) != 1 || modes[0].(map[string]any)["name"] != "resize" || len(modes[0].(map[string]any)["blocked"].([]any)) != 1 {
		t.Fatalf("mode removal did not block its key: %v", result)
	}
	result = c.call("client.keymap", map[string]any{"defaults": []any{layer("defaults", []any{modeBinding}, nil)}, "overrides": []any{layer("settings", nil, []any{map[string]any{"mode": "resize"}})}})
	if modes, _ := result["modes"].([]any); len(modes) != 0 {
		t.Fatalf("whole-mode removal retained the mode: %v", result)
	}
	result = c.call("client.keymap", map[string]any{})
	clear, write := false, false
	for _, value := range result["actions"].([]any) {
		action := value.(map[string]any)
		if action["name"] == "com.superlogical.terminal.clear" {
			clear = action["title"] == "Clear Terminal" && action["args"] == nil
		}
		if action["name"] == "com.superlogical.terminal.write" {
			write = action["title"] == nil && action["args"] != nil && action["description"] != ""
		}
	}
	if !clear || !write {
		t.Fatalf("block method/action metadata: clear=%v write=%v", clear, write)
	}
	for _, input := range []map[string]any{
		{"defaults": []any{layer("bad:name", nil, nil)}},
		{"defaults": []any{layer("same", nil, nil)}, "overrides": []any{layer("same", nil, nil)}},
		{"overrides": []any{layer("settings", []any{map[string]any{"keys": []any{"x"}, "mode": "root", "action": "foo"}}, nil)}},
		{"overrides": []any{layer("settings", nil, []any{map[string]any{"mode": "resize", "action": "foo"}})}},
	} {
		if _, failure := c.exchange("client.keymap", input); failure["code"] != "bad_request" {
			t.Fatalf("invalid keymap accepted: %v: %v", input, failure)
		}
	}
}
