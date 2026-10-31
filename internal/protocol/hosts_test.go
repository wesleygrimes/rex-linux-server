package protocol

import (
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

func checkHosts(t *testing.T, c *testClient) {
	t.Helper()
	base := c.call("hosts.list", map[string]any{})
	t.Logf("empty hosts hash: %v", base["hash"])
	id := "host:" + strings.TrimPrefix(c.welcome["client"].(map[string]any)["id"].(string), "client:")
	host := map[string]any{"id": id, "label": "host-" + id[5:13], "endpoints": []any{"https://host.example.ts.net", "192.0.2.1:7297"}, "icon": "globe", "extra": map[string]any{"keep": true}, "source": "discard.lua:1"}
	put := c.call("hosts.put", map[string]any{"host": host})
	defer c.call("hosts.remove", map[string]any{"id": id})
	stored := put["host"].(map[string]any)
	if _, err := time.Parse(time.RFC3339Nano, stored["updated_at"].(string)); err != nil {
		t.Fatalf("host timestamp: %v", stored)
	}
	if stored["source"] != nil || !reflect.DeepEqual(stored["extra"], host["extra"]) {
		t.Fatalf("host extension fields or source: %v", stored)
	}
	listed := c.call("hosts.list", map[string]any{})
	if listed["hash"] != put["hash"] || listed["hash"] == base["hash"] {
		t.Fatalf("host hash not updated consistently: %v", listed)
	}
	same := maps.Clone(host)
	same["updated_at"] = "2000-01-01T00:00:00Z"
	if got := c.call("hosts.put", map[string]any{"host": same}); !reflect.DeepEqual(got, put) {
		t.Fatalf("identical host write changed state: %v; original %v", got, put)
	}
	for _, scenario := range []struct {
		name string
		host map[string]any
		code string
	}{
		{"invalid id", map[string]any{"id": "bad"}, "bad_request"},
		{"empty label", map[string]any{"label": ""}, "bad_request"},
		{"padded label", map[string]any{"label": " padded "}, "bad_request"},
		{"long label", map[string]any{"label": strings.Repeat("x", 65)}, "bad_request"},
		{"no endpoint", map[string]any{"endpoints": []any{}}, "bad_request"},
		{"local socket", map[string]any{"endpoints": []any{"unix:///tmp/server.sock"}}, "bad_request"},
		{"pipe", map[string]any{"endpoints": []any{"npipe:////./pipe/rex"}}, "bad_request"},
		{"duplicate label", map[string]any{"id": "host:01990000-0000-7000-8000-000000000098", "label": strings.ToUpper(host["label"].(string))}, "conflict"},
	} {
		t.Run("hosts/"+scenario.name, func(t *testing.T) {
			input := maps.Clone(host)
			maps.Copy(input, scenario.host)
			_, failure := c.exchange("hosts.put", map[string]any{"host": input})
			if failure == nil || failure["code"] != scenario.code {
				t.Fatalf("expected %s, got %v", scenario.code, failure)
			}
		})
	}
	removed := c.call("hosts.remove", map[string]any{"id": id})
	if removed["removed"] != true || removed["hash"] != base["hash"] {
		t.Fatalf("host removal failed to restore original list: %v", removed)
	}
	if got := c.call("hosts.remove", map[string]any{"id": id}); got["removed"] != false || got["hash"] != base["hash"] {
		t.Fatalf("removing absent host changed list: %v", got)
	}
}
