package protocol

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Inspect a deployed server without creating sessions or changing its state.
func TestEndpointStatus(t *testing.T) {
	if *testEndpoint == "" {
		t.Skip("requires -endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := dialCompatible(t, ctx, *testEndpoint)
	for _, method := range []string{"server.status", "server.whoami", "session.list"} {
		result := c.call(method, map[string]any{})
		switch method {
		case "server.status":
			if result["instance_id"] == "" || result["instance_id"] == nil || result["host_id"] == nil {
				t.Fatalf("server identity is missing: %v", result)
			}
		case "server.whoami":
			principal, _ := result["principal"].(map[string]any)
			if principal["kind"] == nil || principal["transport"] == nil {
				t.Fatalf("connection identity is missing: %v", result)
			}
		case "session.list":
			if _, ok := result["sessions"].([]any); !ok {
				t.Fatalf("session list is missing: %v", result)
			}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", method, encoded)
	}
}
