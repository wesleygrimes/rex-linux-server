package protocol

import (
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func checkMethodCatalog(t *testing.T, c *testClient) {
	t.Helper()
	expected := strings.Fields(`block.close block.event block.method block_creator.list client.action client.inspect client.key client.keymap client.list client.perform client.reply client.set_label client.set_script data.connect help.all hosts.list hosts.put hosts.remove server.config_reload server.status server.tailscale.disable server.tailscale.enable server.tailscale.login server.tailscale.logout server.tailscale.rename server.version server.whoami session.attach session.attach_all session.bootstrap session.close_layer session.close_window session.create session.describe_block session.destroy session.detach session.detach_block session.float_block session.focus_block session.focus_direction session.focus_next_pane session.focus_next_window session.focus_previous_pane session.focus_previous_window session.focus_window session.help session.list session.list_blocks session.list_clients session.lower_layer session.move_block session.move_window session.new_block session.new_layer session.new_split session.new_window session.raise_layer session.resize_layer session.resize_pane session.set_block_label session.set_label session.set_window_label session.swap_blocks session.view session.zoom_block target.complete target.resolve`)
	actual := []string{}
	for _, value := range c.call("help.all", map[string]any{})["methods"].([]any) {
		method := value.(map[string]any)
		actual = append(actual, method["name"].(string))
		if method["description"] == nil || method["description"] == "" {
			t.Fatalf("method has no description: %v", method["name"])
		}
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("control method catalog differs: %v", actual)
	}
	for _, path := range []string{"/llms.txt", "/llms.html"} {
		request, err := http.NewRequestWithContext(c.ctx, "GET", strings.Replace(c.base, "ws://", "http://", 1)+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(c.base, "wss://") {
			request.URL.Scheme = "https"
		}
		response, err := c.opts.HTTPClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !strings.Contains(string(data), "rex.control.v1") {
			t.Fatalf("API guide %s: status=%d error=%v", path, response.StatusCode, err)
		}
	}
}
