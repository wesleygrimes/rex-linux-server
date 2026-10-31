package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

//go:embed testdata/layout-edges.json
var layoutEdgesFixture []byte

func checkLayoutEdges(t *testing.T, c *testClient) {
	t.Helper()
	aliases := map[string]string{}
	frames := []any{}
	show := func(label string, value any) {
		frames = append(frames, map[string]any{"label": label, "value": value})
	}
	block := func(label string) map[string]any {
		return map[string]any{"block": map[string]any{"label": label, "flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/bin/sleep", "60"}, "login": false}}}
	}
	created := c.call("session.create", map[string]any{"label": "layout edge probe", "initial_windows": []any{map[string]any{"layout": block("a")}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	w := view["focused_window"].(map[string]any)
	a := w["focused_block_id"].(string)
	tiled := w["layers"].([]any)[0].(map[string]any)["layer_id"].(string)
	aliases[a], aliases[tiled] = "A", "TILED"
	splitNumber := 0
	call := func(method string, args map[string]any) map[string]any {
		args["session_id"] = sid
		r, e := c.exchange(method, args)
		if id, ok := r["created_split_id"].(string); ok {
			splitNumber++
			aliases[id] = fmt.Sprintf("SPLIT%d", splitNumber)
		}
		show(method, map[string]any{"result": r, "error": e})
		return r
	}
	state := func(label string) {
		v := c.call("session.view", map[string]any{"session_id": sid})
		w := v["focused_window"].(map[string]any)
		layers := []any{}
		for _, item := range w["layers"].([]any) {
			l := item.(map[string]any)
			layers = append(layers, map[string]any{"id": l["layer_id"], "kind": l["kind"], "bounds": l["bounds"], "blocks": l["blocks"]})
		}
		show(label, map[string]any{"revision": v["revision"], "focus": w["focused_block_id"], "zoom": w["zoomed_block_id"], "layers": layers})
	}
	bounds := map[string]any{"x": .1, "y": .1, "w": .5, "h": .5}
	floated := call("session.float_block", map[string]any{"block_id": a, "bounds": bounds})
	aliases[fmt.Sprint(floated["layer_id"])] = "FLOAT_A"
	call("session.float_block", map[string]any{"block_id": a, "bounds": bounds})
	call("session.resize_layer", map[string]any{"layer_id": tiled, "bounds": bounds})
	call("session.raise_layer", map[string]any{"layer_id": tiled})
	nilLayer := call("session.new_layer", map[string]any{"bounds": bounds, "layout": nil})
	if id, ok := nilLayer["layer_id"].(string); ok {
		call("session.close_layer", map[string]any{"layer_id": id})
	}
	call("session.zoom_block", map[string]any{"block_id": a, "zoom": true})
	call("session.focus_next_pane", map[string]any{"wrap": true})
	state("single pane focus preserves zoom")
	newLayer := call("session.new_layer", map[string]any{"bounds": map[string]any{"x": .6, "y": .6, "w": .3, "h": .3}, "layout": block("b"), "focus": false})
	b := newLayer["block_ids"].([]any)[0].(string)
	aliases[b], aliases[newLayer["layer_id"].(string)] = "B", "FLOAT_B"
	call("session.zoom_block", map[string]any{"zoom": false})
	call("session.focus_direction", map[string]any{"direction": "right"})
	state("right across floating layers")
	call("session.focus_direction", map[string]any{"direction": "right"})
	state("right wraps")
	call("session.move_block", map[string]any{"block_id": a, "anchor_block_id": b, "direction": "horizontal", "side": "after", "ratio": .5})
	call("session.move_block", map[string]any{"block_id": a, "anchor_block_id": b, "direction": "horizontal", "side": "after", "ratio": .5})
	call("session.close_layer", map[string]any{"layer_id": tiled})
	state("after closing tiled layer")
	encoded, _ := json.Marshal(frames)
	pairs := []string{}
	for id, name := range aliases {
		pairs = append(pairs, id, name)
	}
	encoded = []byte(strings.NewReplacer(pairs...).Replace(string(encoded)))
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(layoutEdgesFixture, &want); err != nil {
		t.Fatal(err)
	}
	var clean func(any)
	clean = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, item := range value {
				clean(item)
			}
		case map[string]any:
			for key, item := range value {
				if key == "host_id" {
					value[key] = "HOST"
				} else {
					clean(item)
				}
			}
		}
	}
	clean(got)
	if !reflect.DeepEqual(got, want) {
		actual, expected := got.([]any), want.([]any)
		for i := 0; i < min(len(actual), len(expected)); i++ {
			if !reflect.DeepEqual(actual[i], expected[i]) {
				a, _ := json.Marshal(actual[i])
				e, _ := json.Marshal(expected[i])
				t.Fatalf("layout edge frame %d differs:\n got %s\nwant %s", i, a, e)
			}
		}
		t.Fatalf("layout edge frame counts differ: got %d want %d", len(actual), len(expected))
	}
}
