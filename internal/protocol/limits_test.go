package protocol

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func startTestTarget(t *testing.T, binary string, native bool, extra ...string) string {
	t.Helper()
	root := t.TempDir()
	socket, err := os.CreateTemp("", "rex-limits-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket.Close()
	os.Remove(socket.Name())
	t.Cleanup(func() { os.Remove(socket.Name()) })
	endpoint := "unix://" + socket.Name()
	config := filepath.Join(root, "init.lua")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--listen", endpoint, "--state-dir", filepath.Join(root, "state"), "--config", config}, extra...)
	if native {
		args = append([]string{"server", "run"}, args...)
	}
	log, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+root, "XDG_DATA_HOME="+root, "XDG_CACHE_HOME="+root, "REX_RUNTIME_DIR="+root)
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("test server did not stop")
		}
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			if len(data) > 4000 {
				data = data[len(data)-4000:]
			}
			t.Logf("server log: %s", data)
		}
		log.Close()
	})
	return endpoint
}

func TestResourceLimits(t *testing.T) {
	if *serverBinary == "" && *rexBinary == "" {
		t.Skip("requires -server-binary or -rex-binary")
	}
	for _, target := range []struct {
		name, binary string
		native       bool
	}{{"server", *serverBinary, false}, {"rex", *rexBinary, true}} {
		if target.binary == "" {
			continue
		}
		t.Run(target.name, func(t *testing.T) {
			endpoint := startTestTarget(t, target.binary, target.native, "--max-sessions", "1", "--max-blocks", "2", "--max-windows", "2", "--max-panes", "1")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := dialCompatible(t, ctx, endpoint)
			block := map[string]any{"flavor": "com.superlogical.terminal.shell", "options": map[string]any{"shell": "none", "command": []any{"/bin/sh"}, "login": false}}
			leaf := map[string]any{"block": block}
			split := map[string]any{"split": map[string]any{"direction": "horizontal", "before": leaf, "after": leaf}}
			reject := func(method string, args map[string]any) {
				t.Helper()
				_, failure := c.exchange(method, args)
				if failure == nil {
					t.Fatalf("%s exceeded a resource limit", method)
				}
			}
			reject("session.create", map[string]any{"initial_windows": []any{map[string]any{"layout": split}}})
			if len(c.call("session.list", map[string]any{})["sessions"].([]any)) != 0 {
				t.Fatal("failed initial layout retained a session")
			}
			sid := c.call("session.create", map[string]any{})["session_id"]
			c.call("session.attach", map[string]any{"session_id": sid})
			reject("session.create", map[string]any{})
			w := c.call("session.new_window", map[string]any{"session_id": sid, "layout": leaf})
			revision := w["revision"]
			reject("session.new_window", map[string]any{"session_id": sid, "layout": split})
			view := c.call("session.view", map[string]any{"session_id": sid})
			if view["revision"] != revision || len(c.call("session.list_blocks", map[string]any{"session_id": sid})["blocks"].([]any)) != 1 {
				t.Fatal("rejected layout changed the session or leaked a block")
			}
			c.call("session.new_window", map[string]any{"session_id": sid, "layout": leaf})
			reject("session.new_window", map[string]any{"session_id": sid, "layout": leaf})
			reject("session.new_block", map[string]any{"session_id": sid, "flavor": block["flavor"], "options": block["options"]})
			if len(c.call("session.list_blocks", map[string]any{"session_id": sid})["blocks"].([]any)) != 2 {
				t.Fatal("block limit changed existing blocks")
			}
			c.call("session.destroy", map[string]any{"session_id": sid})
			fresh := c.call("session.create", map[string]any{})["session_id"]
			c.call("session.attach", map[string]any{"session_id": fresh})
			c.call("session.destroy", map[string]any{"session_id": fresh})
		})
	}
}
