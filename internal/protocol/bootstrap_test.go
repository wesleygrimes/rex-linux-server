package protocol

import (
	"reflect"
	"testing"
)

func checkBootstrap(t *testing.T, c *testClient) {
	t.Helper()
	create := func(label string) string {
		sid := c.call("session.create", map[string]any{"label": label})["session_id"].(string)
		c.call("session.attach", map[string]any{"session_id": sid})
		return sid
	}
	one := create("bootstrap one")
	defer c.call("session.destroy", map[string]any{"session_id": one})
	two := create("bootstrap two")
	defer c.call("session.destroy", map[string]any{"session_id": two})
	listed := c.call("session.list", map[string]any{})["sessions"].([]any)
	first := listed[0].(map[string]any)["session_id"].(string)
	unknown := "session:01990000-0000-7000-8000-000000000099"
	for _, scenario := range []struct {
		name     string
		input    map[string]any
		selected string
		attached []string
	}{
		{"default", map[string]any{}, first, nil},
		{"preferred", map[string]any{"preferred_session_id": two}, two, nil},
		{"missing preferred", map[string]any{"preferred_session_id": unknown}, first, nil},
		{"explicit empty", map[string]any{"preferred_session_id": two, "attach_session_ids": []any{}}, "", []string{}},
		{"explicit ordered", map[string]any{"preferred_session_id": one, "attach_session_ids": []any{two, unknown, one, two}}, "", []string{two, one}},
	} {
		t.Run("bootstrap/"+scenario.name, func(t *testing.T) {
			connection := dialCompatible(t, c.ctx, c.endpoint)
			defer connection.conn.CloseNow()
			scenario.input["event_scope"] = "all"
			out := connection.call("session.bootstrap", scenario.input)
			if out["client_id"] != connection.welcome["client"].(map[string]any)["id"] {
				t.Fatalf("bootstrap client identity changed: %v", out)
			}
			if scenario.selected == "" {
				if out["selected_session"] != nil {
					t.Fatalf("explicit attachment selected an extra session: %v", out)
				}
			} else {
				selected, ok := out["selected_session"].(map[string]any)
				if !ok {
					t.Fatalf("no selected session: %v", out)
				}
				if got := selected["session"].(map[string]any)["session_id"]; got != scenario.selected {
					t.Fatalf("selected %v, want %s", got, scenario.selected)
				}
				attachment := selected["attachment"].(map[string]any)
				if !reflect.DeepEqual(selected["view"], attachment["view"]) {
					t.Fatal("selected snapshot and attachment disagree")
				}
			}
			if scenario.attached != nil {
				got := []string{}
				attached, _ := out["attached_sessions"].([]any)
				for _, value := range attached {
					got = append(got, value.(map[string]any)["session"].(map[string]any)["session_id"].(string))
				}
				if !reflect.DeepEqual(got, scenario.attached) {
					t.Fatalf("attached %v, want %v", got, scenario.attached)
				}
			}
		})
	}
}
