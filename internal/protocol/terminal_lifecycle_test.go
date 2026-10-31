package protocol

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func checkTerminalLifecycle(t *testing.T, c *testClient) {
	t.Helper()
	window := func(command string, policy map[string]any) map[string]any {
		options := map[string]any{"command": []any{"/bin/sh", "-c", command}, "login": false}
		if policy != nil {
			options["exit"] = policy
		}
		return map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": options}}}
	}
	created := c.call("session.create", map[string]any{"label": "child completion", "initial_windows": []any{
		window("exit 7", nil), window("sleep .35; exit 0", nil),
		window("sleep .35; exit 3", map[string]any{"on_completion": false}),
		window("sleep .35; exit 3", map[string]any{"quick_exit_threshold_ms": 2000}),
	}})
	sid := created["session_id"]
	c.call("session.attach", map[string]any{"session_id": sid})
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	windows := created["initial_windows"].([]any)
	bids := []any{}
	for _, window := range windows {
		bids = append(bids, window.(map[string]any)["block_ids"].([]any)[0])
	}
	for _, i := range []int{0, 2, 3} {
		var result map[string]any
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			result = c.call("com.superlogical.terminal.process", map[string]any{"session_id": sid, "block_id": bids[i], "args": map[string]any{}})
			if result["last_exit"] != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if result["last_exit"] == nil {
			t.Fatalf("retained terminal has no exit result: %v", result)
		}
		code := []float64{7, 0, 3, 3}[i]
		if result["last_exit"].(map[string]any)["exit_code"] != code {
			t.Fatalf("child exit code: %v", result)
		}
	}
	// A newly attached data stream must restore the retained exit state.
	ticket := c.call("data.connect", map[string]any{"session_id": sid, "block_id": bids[0], "data_ref_id": "terminal", "subprotocol": "rex.data.terminal.v1"})
	data, _ := c.data(ticket["ticket"].(string))
	defer data.CloseNow()
	streamCtx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	var restored []byte
	for !bytes.Contains(restored, []byte(`"type":"child_exited"`)) {
		_, chunk, err := data.Read(streamCtx)
		if err != nil {
			t.Fatal(err)
		}
		restored = append(restored, chunk...)
	}
	if !bytes.Contains(restored, []byte(`"exit_code":7`)) {
		t.Fatalf("retained exit marker missing code: %q", restored[len(restored)-min(len(restored), 200):])
	}
	for deadline := time.Now().Add(time.Second); ; {
		found := false
		for _, item := range c.call("session.list_blocks", map[string]any{"session_id": sid})["blocks"].([]any) {
			found = found || item.(map[string]any)["block_id"] == bids[1]
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed non-quick terminal did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
