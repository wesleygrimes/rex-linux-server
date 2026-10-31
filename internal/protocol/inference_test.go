package protocol

import (
	"strings"
	"testing"
	"time"
)

func checkProgramInference(t *testing.T, c *testClient, path string) {
	t.Helper()
	created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []any{path, "terminal"}, "login": false, "exit": map[string]any{"on_completion": false}}}}}}})
	sid := created["session_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	args := func(a map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": a}
	}
	for deadline := time.Now().Add(time.Second); ; {
		content := c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"].(string)
		if strings.Contains(content, "INFERENCE_READY") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inference fixture did not start: %q", content)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, command := range []string{"b", "w", "d", "p", "r"} {
		c.call("com.superlogical.terminal.write", args(map[string]any{"data": command}))
		time.Sleep(150 * time.Millisecond)
		status := c.call("com.superlogical.terminal.program_status", args(map[string]any{}))
		want := map[string]string{"b": "blocked", "w": "working", "d": "done", "p": "done", "r": "done"}[command]
		records := status["records"].([]any)
		if len(records) != 1 {
			t.Fatalf("inference %s missing record: %v", command, status)
		}
		record := records[0].(map[string]any)
		if record["state"] != want || record["app"] != "codex" || record["effective_app"] != "codex" {
			t.Fatalf("inference %s: %v", command, status)
		}
		if command == "b" && record["kind"] != "permission" {
			t.Fatalf("approval kind: %v", record)
		}
	}
}
