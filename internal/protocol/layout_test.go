package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Expected frames are fixed protocol fixtures, with IDs and connection
// metadata normalized.
//
//go:embed testdata/layout.json
var layoutFixture []byte

func checkLayout(t *testing.T, c *testClient) {
	t.Helper()
	aliases := map[string]string{}
	aliases[c.welcome["client"].(map[string]any)["id"].(string)] = "CLIENT"
	frames := []any{}
	logValue := func(label string, value any) {
		if state, ok := value.(map[string]any); ok && state["windows"] != nil {
			var active any
			for _, item := range state["windows"].([]any) {
				if item.(map[string]any)["window_id"] == state["active_window_id"] {
					active = item
				}
			}
			if !reflect.DeepEqual(state["focused_window"], active) {
				t.Fatalf("focused window disagrees with window list: %v", state)
			}
		}
		frames = append(frames, map[string]any{"label": label, "value": value})
	}
	block := func(label string) map[string]any {
		return map[string]any{"block": map[string]any{"label": label, "flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}}}
	}
	created := c.call("session.create", map[string]any{"label": "layout probe", "initial_windows": []any{map[string]any{"window_label": "one", "layout": block("a")}, map[string]any{"window_label": "two", "layout": block("b")}}})
	sid := created["session_id"].(string)
	aliases[sid] = "SESSION"
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	windows := created["initial_windows"].([]any)
	w1 := windows[0].(map[string]any)["window_id"].(string)
	w2 := windows[1].(map[string]any)["window_id"].(string)
	a := windows[0].(map[string]any)["block_ids"].([]any)[0].(string)
	b := windows[1].(map[string]any)["block_ids"].([]any)[0].(string)
	aliases[w1], aliases[w2], aliases[a], aliases[b] = "W1", "W2", "A", "B"
	splitNumber := 0
	call := func(method string, args map[string]any) map[string]any {
		args["session_id"] = sid
		result, failure := c.exchange(method, args)
		if id, ok := result["created_split_id"].(string); ok {
			splitNumber++
			aliases[id] = fmt.Sprintf("SPLIT%d", splitNumber)
		}
		logValue(method, map[string]any{"result": result, "error": failure})
		return result
	}
	view := func(label string) map[string]any {
		result := c.call("session.view", map[string]any{"session_id": sid})
		logValue(label, result)
		return result
	}
	logValue("created", created)
	attached := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	for i, value := range attached["windows"].([]any) {
		layer := value.(map[string]any)["layers"].([]any)[0].(map[string]any)
		aliases[layer["layer_id"].(string)] = fmt.Sprintf("TILED%d", i+1)
	}
	logValue("initial", attached)
	call("session.focus_window", map[string]any{"window_id": w1})
	loose := call("session.new_block", map[string]any{"flavor": "com.superlogical.terminal.shell", "label": "loose", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}})["block_id"].(string)
	aliases[loose] = "LOOSE"
	call("session.list_blocks", map[string]any{})
	bounds := func(x, y, w, h float64) map[string]any { return map[string]any{"x": x, "y": y, "w": w, "h": h} }
	call("session.float_block", map[string]any{"block_id": loose, "bounds": bounds(.1, .2, .6, .5)})
	// Floating requires a block already placed in a window.
	call("session.move_block", map[string]any{"block_id": loose, "anchor_block_id": a, "direction": "horizontal", "side": "after", "ratio": .5})
	looseLayer := call("session.float_block", map[string]any{"block_id": loose, "bounds": bounds(.1, .2, .6, .5)})["layer_id"].(string)
	aliases[looseLayer] = "FLOAT_LOOSE"
	view("after float loose")
	layer := call("session.new_layer", map[string]any{"window_id": w1, "bounds": bounds(.2, .3, .4, .4), "layout": block("d"), "focus": false})
	if id, ok := layer["layer_id"].(string); ok {
		aliases[id] = "FLOAT_D"
		aliases[layer["block_ids"].([]any)[0].(string)] = "D"
	}
	view("after new layer")
	floatA := call("session.float_block", map[string]any{"block_id": a, "bounds": bounds(.05, .05, .5, .5)})["layer_id"].(string)
	aliases[floatA] = "FLOAT_A"
	view("after float A")
	call("session.lower_layer", map[string]any{"layer_id": floatA, "relative_layer_id": looseLayer})
	view("after lower A")
	call("session.raise_layer", map[string]any{"layer_id": floatA})
	call("session.resize_layer", map[string]any{"layer_id": looseLayer, "bounds": bounds(0, 0, 0, 1)})
	call("session.resize_layer", map[string]any{"layer_id": looseLayer, "bounds": bounds(2, 0, 1, 1)})
	call("session.resize_layer", map[string]any{"layer_id": looseLayer, "bounds": bounds(.15, .1, .5, .6)})
	call("session.zoom_block", map[string]any{"block_id": loose, "zoom": true})
	view("after zoom")
	call("session.zoom_block", map[string]any{"window_id": w1, "zoom": false})
	call("session.detach_block", map[string]any{"block_id": loose})
	call("session.list_blocks", map[string]any{})
	view("after detach")
	call("session.move_block", map[string]any{"block_id": loose, "anchor_block_id": b, "direction": "horizontal", "side": "before", "ratio": .3})
	view("after move")
	call("session.swap_blocks", map[string]any{"block_id": loose, "other_block_id": b})
	view("after swap")
	call("session.focus_block", map[string]any{"block_id": b})
	call("session.resize_pane", map[string]any{"direction": "horizontal", "before_block_id": b, "after_block_id": loose, "ratio": .75})
	call("session.focus_direction", map[string]any{"direction": "right"})
	view("after focus right")
	call("session.focus_next_pane", map[string]any{"wrap": true})
	call("session.focus_previous_pane", map[string]any{"wrap": false})
	call("session.focus_next_window", map[string]any{"wrap": false})
	call("session.focus_previous_window", map[string]any{"wrap": true})
	call("session.move_window", map[string]any{"window_id": w2, "before_window_id": w1})
	view("after tab move")
	call("session.close_layer", map[string]any{"layer_id": floatA})
	view("after close float A")
	call("session.list_clients", map[string]any{})
	methods := c.call("session.help", map[string]any{"session_id": sid})
	keys := []string{}
	for key := range methods {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	logValue("session help keys", keys)
	encoded, _ := json.Marshal(frames)
	pairs := []string{}
	for id, alias := range aliases {
		pairs = append(pairs, id, alias)
	}
	encoded = []byte(strings.NewReplacer(pairs...).Replace(string(encoded)))
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(layoutFixture, &want); err != nil {
		t.Fatal(err)
	}
	var clean func(any) any
	clean = func(value any) any {
		switch value := value.(type) {
		case []any:
			for i, item := range value {
				value[i] = clean(item)
			}
		case map[string]any:
			for key, item := range value {
				switch key {
				case "focused_window", "connected_at", "principal":
					delete(value, key)
				case "host_id":
					value[key] = "HOST"
				default:
					value[key] = clean(item)
				}
			}
		}
		return value
	}
	got = clean(got)
	if !reflect.DeepEqual(got, want) {
		actual, expected := got.([]any), want.([]any)
		for i := 0; i < min(len(actual), len(expected)); i++ {
			if !reflect.DeepEqual(actual[i], expected[i]) {
				a, _ := json.Marshal(actual[i])
				e, _ := json.Marshal(expected[i])
				t.Fatalf("layout frame %d differs:\n got %s\nwant %s", i, a, e)
			}
		}
		t.Fatalf("layout frame counts differ: got %d want %d", len(actual), len(expected))
	}
}
