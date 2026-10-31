package protocol

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func checkListDir(t *testing.T, c *testClient) {
	t.Helper()
	program := `import os, signal, socket, sys, tempfile
signal.signal(signal.SIGHUP, lambda *args: sys.exit(0))
with tempfile.TemporaryDirectory(prefix="rex-directory-") as root:
    root = os.path.realpath(root)
    for name in ["Alpha", "apple", ".hidden", "zoo", "denied"]:
        os.mkdir(os.path.join(root, name))
    os.chmod(os.path.join(root, "denied"), 0)
    open(os.path.join(root, "afile"), "w").close()
    os.symlink("Alpha", os.path.join(root, "linkdir"))
    os.symlink("afile", os.path.join(root, "linkfile"))
    os.symlink("missing", os.path.join(root, "broken"))
    os.chdir(root)
    os.write(1, ("\x1b]7;file://" + socket.gethostname() + root + "\x07DIR_ROOT:" + root + "\r\n").encode())
    while True:
        os.read(0, 4096)
`
	created := c.call("session.create", map[string]any{"label": "directory probe", "initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []string{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"].(string)
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"].(string)
	root := ""
	for i := 0; i < 100; i++ {
		screen := fmt.Sprint(c.call("com.superlogical.terminal.format", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"trim": true, "unwrap": true}})["content"])
		if _, tail, found := strings.Cut(screen, "DIR_ROOT:"); found {
			root = strings.TrimSpace(strings.SplitN(tail, "\n", 2)[0])
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if root == "" {
		t.Fatal("directory fixture did not start")
	}
	directories := []string{".hidden", "Alpha", "apple", "denied", "linkdir:symlink", "zoo"}
	for _, test := range []struct {
		args      map[string]any
		status    string
		names     []string
		truncated bool
	}{
		{map[string]any{"path": root}, "ok", directories, false},
		{map[string]any{"path": root, "files": true}, "ok", []string{".hidden", "Alpha", "apple", "denied", "linkdir:symlink", "zoo", "afile:file", "linkfile:file:symlink"}, false},
		{map[string]any{"path": root, "prefix": "a"}, "ok", []string{"Alpha", "apple"}, false},
		{map[string]any{"path": root, "prefix": "A", "files": true}, "ok", []string{"Alpha", "apple", "afile:file"}, false},
		{map[string]any{"path": root, "limit": 1}, "ok", nil, true},
		{map[string]any{"path": root, "limit": -1}, "ok", directories, false},
		{map[string]any{"path": root + "/missing"}, "missing", []string{}, false},
		{map[string]any{"path": root + "/afile"}, "not_dir", []string{}, false},
		{map[string]any{"path": root + "/denied"}, "denied", []string{}, false},
		{map[string]any{"path": "Alpha"}, "ok", []string{}, false},
		{map[string]any{"path": "~other"}, "missing", []string{}, false},
	} {
		out := c.call("com.superlogical.terminal.list_dir", map[string]any{"session_id": sid, "block_id": bid, "args": test.args})
		if out["status"] != test.status || out["truncated"] != test.truncated || out["home"] == "" {
			t.Fatalf("directory case %v: %v", test.args, out)
		}
		names := []string{}
		for _, value := range out["entries"].([]any) {
			entry := value.(map[string]any)
			name := entry["name"].(string)
			if entry["file"] == true {
				name += ":file"
			}
			if entry["symlink"] == true {
				name += ":symlink"
			}
			names = append(names, name)
		}
		if test.truncated {
			if len(names) != 1 {
				t.Fatalf("directory limit: %v", out)
			}
		} else {
			sort.Strings(names)
			sort.Strings(test.names)
			if !reflect.DeepEqual(names, test.names) {
				t.Fatalf("directory case %v: names=%v want=%v", test.args, names, test.names)
			}
		}
		if test.args["path"] == "Alpha" && out["path"] != root+"/Alpha" {
			t.Fatalf("path was not resolved relative to the child: %v", out)
		}
	}
}
