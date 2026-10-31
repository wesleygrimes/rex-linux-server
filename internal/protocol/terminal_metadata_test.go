package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func checkTerminalLaunch(t *testing.T, c *testClient) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	name := "rex-command-probe"
	program := "#!/usr/bin/python3\nimport os,sys\nprint('ARG:'+sys.argv[1],flush=True)\nprint('CWD:'+os.getcwd(),flush=True)\nprint('TERM:'+os.environ.get('TERM_PROGRAM',''),flush=True)\n"
	if err := os.WriteFile(filepath.Join(root, name), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"none", "/bin/sh"} {
		options := map[string]any{"shell": shell, "command": []any{name, "spaces $HOME; `literal`"}, "path_prepend": []any{root}, "cwd": root, "login": false, "exit": map[string]any{"on_completion": false}}
		created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": options}}}}})
		sid := created["session_id"]
		view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
		bid := view["focused_window"].(map[string]any)["focused_block_id"]
		for deadline := time.Now().Add(2 * time.Second); ; {
			content := c.call("com.superlogical.terminal.format", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"trim": true, "unwrap": true}})["content"].(string)
			if strings.Contains(content, "TERM:rex") {
				if !strings.Contains(content, "ARG:spaces $HOME; `literal`") || !strings.Contains(content, "CWD:"+root) {
					t.Fatalf("command arguments or directory changed: %q", content)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("command did not start through shell %q: %q", shell, content)
			}
			time.Sleep(5 * time.Millisecond)
		}
		c.call("session.destroy", map[string]any{"session_id": sid})
	}
}

func checkTerminalMetadata(t *testing.T, c *testClient) {
	t.Helper()
	program := `import os,tty
 tty.setraw(0)
 os.write(1,b'METADATA_READY\r\n')
 while True:
  command=os.read(0,1)
  if command==b't': os.write(1,b'\x1b]2;Metadata title\x07')
  elif command==b'w': os.write(1,b'\x1b]7;file://localhost/tmp\x07')
  elif command==b'p': os.write(1,b'\x1b]9;4;1;37\x07')
  elif command==b'r': os.write(1,b'\x1b]9;4;0\x07')
  elif command==b'b': os.write(1,b'\x07\x1b]777;notify;Notice;Message\x07')
  os.write(1,b'METADATA_SENT:'+command+b'\r\n')
 `
	// Keep the fixture independent of the account's shell initialization.
	program = strings.ReplaceAll(program, "\n ", "\n")
	created := c.call("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"command": []any{"/usr/bin/python3", "-u", "-c", program}, "login": false}}}}}})
	sid := created["session_id"]
	defer c.call("session.destroy", map[string]any{"session_id": sid})
	view := c.call("session.attach", map[string]any{"session_id": sid})["view"].(map[string]any)
	bid := view["focused_window"].(map[string]any)["focused_block_id"]
	args := func(a map[string]any) map[string]any {
		return map[string]any{"session_id": sid, "block_id": bid, "args": a}
	}
	wait := func(text string) {
		t.Helper()
		for deadline := time.Now().Add(time.Second); ; {
			content := c.call("com.superlogical.terminal.format", args(map[string]any{"trim": true}))["content"].(string)
			if strings.Contains(content, text) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("metadata fixture missing %q: %q", text, content)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait("METADATA_READY")

	child := c.call("com.superlogical.terminal.process", args(map[string]any{}))["child"].(map[string]any)
	for _, command := range []string{"t", "w", "p", "r", "b"} {
		c.call("com.superlogical.terminal.write", args(map[string]any{"data": command}))
		wait("METADATA_SENT:" + command)
		title := c.call("com.superlogical.terminal.title", args(map[string]any{}))
		owner, _ := title["owner"].(map[string]any)
		if title["title"] != "Metadata title" || owner["pid"] != child["pid"] || owner["name"] == nil {
			t.Fatalf("title owner: %v", title)
		}
		if command != "t" {
			pwd := c.call("com.superlogical.terminal.pwd", args(map[string]any{}))
			owner, _ := pwd["owner"].(map[string]any)
			if pwd["pwd"] != "file://localhost/tmp" || owner["pid"] != child["pid"] {
				t.Fatalf("working directory owner: %v", pwd)
			}
		}
		if command == "p" || command == "r" {
			status := c.call("com.superlogical.terminal.program_status", args(map[string]any{}))
			records := status["records"].([]any)
			state := "working"
			if command == "r" {
				state = "done"
			}
			if len(records) != 1 || records[0].(map[string]any)["state"] != state {
				t.Fatalf("progress status %s: %v", command, status)
			}
		}
	}
	expected := map[string]bool{"title_changed": false, "pwd_changed": false, "progress_report": false, "bell": false, "desktop_notification": false}
	for deadline := time.Now().Add(time.Second); ; {
		c.call("server.status", map[string]any{})
		for _, outer := range c.events {
			if outer["type"] != "session_event" {
				continue
			}
			event := outer["payload"].(map[string]any)["event"].(map[string]any)
			if event["type"] != "block_event" {
				continue
			}
			block := event["payload"].(map[string]any)
			if block["block_id"] != bid {
				continue
			}
			name, _ := block["name"].(string)
			if _, ok := expected[name]; !ok {
				continue
			}
			payload := block["payload"].(map[string]any)
			if name == "progress_report" {
				if payload["state"] != "set" || payload["progress"] != float64(37) {
					continue
				}
			}
			if name == "desktop_notification" && (payload["title"] != "Notice" || payload["body"] != "Message") {
				t.Fatalf("notification: %v", payload)
			}
			expected[name] = true
		}
		complete := true
		for _, found := range expected {
			complete = complete && found
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal notifications missing: %v", expected)
		}
		time.Sleep(time.Millisecond)
	}
}
