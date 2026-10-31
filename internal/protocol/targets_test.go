package protocol

import (
	"strings"
	"testing"
)

func checkTargets(t *testing.T, c *testClient) {
	t.Helper()
	window := func(label string) map[string]any {
		return map[string]any{"window_label": label, "layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}}}}
	}
	created := c.call("session.create", map[string]any{"label": "resolve-alpha", "initial_windows": []any{window("first"), window("second")}})
	sid := created["session_id"].(string)
	c.call("session.attach", map[string]any{"session_id": sid})
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	queries := []any{"RESOLVE-ALPHA", sid, strings.ReplaceAll(sid, "-", "")[len(strings.ReplaceAll(sid, "-", ""))-6:]}
	resolved := c.call("target.resolve", map[string]any{"kinds": []any{"session"}, "queries": queries})["results"].([]any)
	for _, value := range resolved {
		result := value.(map[string]any)
		if result["status"] != "resolved" || result["target"].(map[string]any)["session_id"] != sid {
			t.Fatalf("session query: %v", result)
		}
	}
	windows := created["initial_windows"].([]any)
	windowID := windows[0].(map[string]any)["window_id"]
	resolvedWindow := c.call("target.resolve", map[string]any{"kinds": []any{"window"}, "queries": []any{"1"}, "scope": map[string]any{"session": sid}})["results"].([]any)[0].(map[string]any)
	if resolvedWindow["status"] != "resolved" || resolvedWindow["target"].(map[string]any)["window_id"] != windowID {
		t.Fatalf("window ordinal: %v", resolvedWindow)
	}
	loose := c.call("session.new_block", map[string]any{"session_id": sid, "flavor": "com.superlogical.terminal.shell", "label": "Loose", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}})["block_id"]
	result := c.call("target.complete", map[string]any{"kinds": []any{"block"}, "prefix": " lo ", "scope": map[string]any{"session": sid}})
	completions := result["completions"].([]any)
	if len(completions) != 1 {
		t.Fatalf("detached block completion: %v", result)
	}
	completion := completions[0].(map[string]any)
	if completion["value"] != "Loose" || completion["form"] != "label" || completion["target"].(map[string]any)["block_id"] != loose || completion["target"].(map[string]any)["where"] != "detached" {
		t.Fatalf("detached block completion fields: %v", completion)
	}
	clientID := c.welcome["client"].(map[string]any)["id"]
	c.call("client.set_label", map[string]any{"label": "target-client"})
	defer c.call("client.set_label", map[string]any{"label": ""})
	client := c.call("target.resolve", map[string]any{"kinds": []any{"client"}, "queries": []any{"TARGET-CLIENT"}})["results"].([]any)[0].(map[string]any)
	if client["status"] != "resolved" || client["target"].(map[string]any)["client_id"] != clientID {
		t.Fatalf("client label resolution: %v", client)
	}
	for _, kinds := range [][]any{{}, {"bad"}, {"session", "session"}, {"client", "block"}} {
		if _, failure := c.exchange("target.complete", map[string]any{"kinds": kinds, "prefix": ""}); failure == nil || failure["code"] != "bad_request" {
			t.Fatalf("invalid target kinds accepted: %v: %v", kinds, failure)
		}
	}
	second := c.call("session.create", map[string]any{"label": "resolve-beta", "initial_windows": []any{window("first")}})
	secondID := second["session_id"]
	c.call("session.attach", map[string]any{"session_id": secondID})
	defer c.call("session.destroy", map[string]any{"session_id": secondID})
	firstBlock := windows[0].(map[string]any)["block_ids"].([]any)[0]
	secondWindow := windows[1].(map[string]any)["window_id"]
	secondBlock := windows[1].(map[string]any)["block_ids"].([]any)[0]
	resolve := func(kinds []any, query string, context, scope map[string]any) map[string]any {
		a := map[string]any{"kinds": kinds, "queries": []any{query}}
		if context != nil {
			a["context"] = context
		}
		if scope != nil {
			a["scope"] = scope
		}
		return c.call("target.resolve", a)["results"].([]any)[0].(map[string]any)
	}
	for _, kind := range []string{"session", "window", "block"} {
		if got := resolve([]any{kind}, "", nil, nil); got["status"] != "ambiguous" {
			t.Fatalf("default %s across sessions: %v", kind, got)
		}
	}
	for _, item := range []struct {
		kinds          []any
		query          string
		context, scope map[string]any
		key            string
		want           any
	}{
		{[]any{"session"}, "", map[string]any{"session_id": sid}, nil, "session_id", sid},
		{[]any{"window"}, "", map[string]any{"session_id": sid, "block_id": firstBlock}, nil, "window_id", windowID},
		{[]any{"block"}, "", map[string]any{"session_id": sid}, map[string]any{"window": "second"}, "block_id", secondBlock},
		{[]any{"block", "session", "window"}, "", map[string]any{"session_id": sid, "block_id": firstBlock}, nil, "block_id", firstBlock},
		{[]any{"window"}, "second", map[string]any{"session_id": sid}, nil, "window_id", secondWindow},
		{[]any{"client"}, "", map[string]any{"client_id": clientID}, nil, "client_id", clientID},
	} {
		got := resolve(item.kinds, item.query, item.context, item.scope)
		if got["status"] != "resolved" || got["target"].(map[string]any)[item.key] != item.want {
			t.Fatalf("default/scoped target: %+v: %v", item, got)
		}
	}
	if got := resolve([]any{"client"}, "", nil, nil); got["status"] != "not_found" {
		t.Fatalf("CLI chosen by default: %v", got)
	}
	if got := resolve([]any{"window"}, "first", nil, nil); got["status"] != "ambiguous" {
		t.Fatalf("duplicate window labels: %v", got)
	}
	if got := resolve([]any{"block"}, "", nil, map[string]any{"session": "absent"}); got["status"] != "not_found" {
		t.Fatalf("missing session scope: %v", got)
	}
	for _, scope := range []map[string]any{{"session": "absent"}, {"window": "first"}} {
		if got := c.call("target.complete", map[string]any{"kinds": []any{"block"}, "prefix": "", "scope": scope})["completions"].([]any); len(got) != 0 {
			t.Fatalf("unresolved scope returned completions: %v", got)
		}
	}
	c.call("session.set_window_label", map[string]any{"session_id": sid, "window_id": windowID, "label": secondID})
	if got := resolve([]any{"window"}, secondID.(string), map[string]any{"session_id": sid}, nil); got["status"] != "not_found" {
		t.Fatalf("full ID fell back to a label of another kind: %v", got)
	}
	c.call("session.set_window_label", map[string]any{"session_id": sid, "window_id": windowID, "label": "2"})
	if got := resolve([]any{"window"}, "2", map[string]any{"session_id": sid}, nil); got["target"].(map[string]any)["window_id"] != windowID {
		t.Fatalf("ordinal took precedence over label: %v", got)
	}
}
