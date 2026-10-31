package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBlockPlugins(t *testing.T) {
	if *rexBinary == "" && *serverBinary == "" {
		t.Skip("requires -server-binary or -rex-binary")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "plugins")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "echo")
	build := exec.Command("go", "build", "-o", plugin, "./testdata/plugin")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building plugin fixture: %v: %s", err, output)
	}
	for _, target := range []struct {
		name, binary     string
		native, launcher bool
	}{{"server", *serverBinary, false, false}, {"rex", *rexBinary, true, false}, {"server launcher", *serverBinary, false, true}, {"rex launcher", *rexBinary, true, true}} {
		if target.binary == "" {
			continue
		}
		t.Run(target.name, func(t *testing.T) {
			state := t.TempDir()
			activeDir := filepath.Join(state, "plugins")
			if err := os.Mkdir(activeDir, 0700); err != nil {
				t.Fatal(err)
			}
			activePlugin := filepath.Join(activeDir, "echo")
			fixture, err := os.ReadFile(plugin)
			if err != nil {
				t.Fatal(err)
			}
			if target.launcher {
				fixture = []byte("#!/bin/sh\n'" + strings.ReplaceAll(plugin, "'", "'\\''") + "'\n")
			}
			if err := os.WriteFile(activePlugin, fixture, 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(state, "init.lua")
			os.WriteFile(config, []byte("rex.action{name='plugin_echo',title='Plugin echo',run=function(ctx,args) local result,err=rex.block.call('dev.example.echo','echo',{message=args.message});if not result then error(err) end;return result end}"), 0600)
			f, err := os.CreateTemp("", "rex-plugin-*.sock")
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			os.Remove(f.Name())
			defer os.Remove(f.Name())
			endpoint := "unix://" + f.Name()
			args := []string{"--listen", endpoint, "--state-dir", filepath.Join(state, "state"), "--config", config, "--block-dir", activeDir, "--block-plugin-reload"}
			if target.native {
				args = append([]string{"server", "run"}, args...)
			}
			cmd := exec.Command(target.binary, args...)
			log, err := os.Create(filepath.Join(state, "server.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+state, "XDG_DATA_DIRS="+state, "REX_TEST_PLUGIN_LOG="+filepath.Join(state, "plugin.log"))
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cmd.Process.Signal(syscall.SIGINT)
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					cmd.Process.Kill()
					<-done
				}
				if t.Failed() {
					data, _ := os.ReadFile(log.Name())
					if len(data) > 8000 {
						data = data[len(data)-8000:]
					}
					t.Logf("server log: %s", data)
					data, _ = os.ReadFile(filepath.Join(state, "plugin.log"))
					if len(data) > 4000 {
						data = data[len(data)-4000:]
					}
					t.Logf("plugin requests: %s", data)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := dialCompatible(t, ctx, endpoint)
			creators := c.call("block_creator.list", map[string]any{})["creators"].([]any)
			found := false
			names := []any{}
			for _, value := range creators {
				names = append(names, value.(map[string]any)["name"])
				found = found || value.(map[string]any)["name"] == "dev.example.echo"
			}
			if !found {
				t.Fatalf("executable creator was not discovered: %v", names)
			}
			checkHelp := func(catalog map[string]any) {
				t.Helper()
				for _, value := range catalog["block_types"].([]any) {
					kind := value.(map[string]any)
					if kind["creator"] != "dev.example.echo" {
						continue
					}
					methods := kind["methods"].([]any)
					if len(methods) != 2 || methods[0].(map[string]any)["name"] != "echo" || methods[1].(map[string]any)["name"] != "mirror" {
						t.Fatalf("plugin help methods: %v", methods)
					}
					return
				}
				t.Fatal("plugin is missing from API help")
			}
			checkHelp(c.call("help.all", map[string]any{}))
			request, err := http.NewRequestWithContext(ctx, "GET", "http://rex/api/help", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := c.opts.HTTPClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var catalog map[string]any
			err = json.NewDecoder(response.Body).Decode(&catalog)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			checkHelp(catalog)
			sid := c.call("session.create", map[string]any{})["session_id"]
			c.call("session.attach", map[string]any{"session_id": sid})
			checkHelp(c.call("session.help", map[string]any{"session_id": sid}))
			defer c.call("session.destroy", map[string]any{"session_id": sid})
			bid := c.call("session.new_block", map[string]any{"session_id": sid, "flavor": "dev.example.echo.echo", "label": "Echo", "options": map[string]any{}})["block_id"]
			result := c.call("block.method", map[string]any{"session_id": sid, "block_id": bid, "method": "echo", "args": map[string]any{"message": "hello plugin"}})
			if result["message"] != "hello plugin" {
				t.Fatalf("plugin method result: %v", result)
			}
			for _, value := range []any{nil, "text", []any{float64(3), true}, map[string]any{"nested": []any{false, nil}}} {
				out := c.call("block.method", map[string]any{"session_id": sid, "block_id": bid, "method": "mirror", "args": value})
				if !reflect.DeepEqual(out["value"], value) {
					t.Fatalf("plugin changed JSON input: got %#v want %#v", out, value)
				}
			}
			described := c.call("session.describe_block", map[string]any{"session_id": sid, "block_id": bid})
			if described["block"].(map[string]any)["creator_name"] != "dev.example.echo" || described["api"].(map[string]any)["methods"].(map[string]any)["echo"] == nil {
				t.Fatalf("plugin metadata: %v", described)
			}
			result = c.call("dev.example.echo.echo", map[string]any{"session_id": sid, "block_id": bid, "args": map[string]any{"message": "namespaced"}})
			if result["message"] != "namespaced" {
				t.Fatalf("namespaced plugin call: %v", result)
			}
			for _, item := range []struct {
				action, want string
				args         map[string]any
			}{{"dev.example.echo.echo", "action", map[string]any{"message": "action"}}, {"dev.example.echo.greet", "fixed greeting", nil}, {"plugin_echo", "Lua", map[string]any{"message": "Lua"}}} {
				input := map[string]any{"session_id": sid, "block_id": bid, "action": item.action}
				if item.args != nil {
					input["args"] = item.args
				}
				out := c.call("client.action", input)
				result, ok := out["result"].(map[string]any)
				if !ok {
					t.Fatalf("plugin action %s omitted result: %v", item.action, out)
				}
				if result["message"] != item.want {
					t.Fatalf("plugin action %s: %v", item.action, result)
				}
			}
			if _, failure := c.exchange("client.action", map[string]any{"session_id": sid, "block_id": bid, "action": "dev.example.echo.greet", "args": map[string]any{"message": "override"}}); failure["code"] != "bad_request" {
				t.Fatalf("declared action accepted argument overrides: %v", failure)
			}
			if _, failure := c.exchange("data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "echo", "subprotocol": "rex.data.terminal.v1"}); failure["code"] != "not_found" {
				t.Fatalf("plugin without data provider: %v", failure)
			}

			c.call("block.event", map[string]any{"session_id": sid, "block_id": bid, "event": map[string]any{"type": "paste", "payload": map[string]any{"text": "event"}}})
			for deadline := time.Now().Add(time.Second); ; {
				c.call("server.status", map[string]any{})
				found := false
				for _, outer := range c.events {
					if outer["type"] != "session_event" {
						continue
					}
					event := outer["payload"].(map[string]any)["event"].(map[string]any)
					if event["type"] != "block_event" {
						continue
					}
					payload := event["payload"].(map[string]any)
					if payload["block_id"] == bid && payload["name"] == "echoed" {
						found = payload["payload"].(map[string]any)["message"] == "event"
					}
				}
				if found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("plugin event did not reach attached client")
				}
				time.Sleep(time.Millisecond)
			}
			window := c.call("session.new_window", map[string]any{"session_id": sid, "layout": map[string]any{"block": map[string]any{"flavor": "dev.example.echo.echo", "options": map[string]any{}}}})
			placed := window["block_ids"].([]any)[0]
			view := c.call("session.view", map[string]any{"session_id": sid})
			block := view["focused_window"].(map[string]any)["layers"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)
			if block["block_id"] != placed || block["flavor"] != "dev.example.echo.echo" {
				t.Fatalf("placed plugin metadata: %v", block)
			}
			c.call("block.close", map[string]any{"session_id": sid, "block_id": bid})
			other := dialCompatible(t, ctx, endpoint)
			other.call("session.attach", map[string]any{"session_id": sid})
			other.conn.CloseNow()
			time.Sleep(30 * time.Millisecond)
			replacement := filepath.Join(state, "echo-replacement")
			build := exec.Command("go", "build", "-ldflags", "-X main.version=updated", "-o", replacement, "./testdata/plugin")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("building replacement plugin: %v: %s", err, output)
			}
			if err := os.Rename(replacement, activePlugin); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(5 * time.Second); ; {
				result, failure := c.exchange("block.method", map[string]any{"session_id": sid, "block_id": placed, "method": "echo", "args": map[string]any{"message": "after reload"}})
				if failure == nil && result["version"] == "updated" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("existing plugin block did not recover after reload: %v %v", result, failure)
				}
				time.Sleep(30 * time.Millisecond)
			}
			view = c.call("session.view", map[string]any{"session_id": sid})
			if view["focused_window"].(map[string]any)["focused_block_id"] != placed {
				t.Fatalf("plugin reload changed the layout: %v", view)
			}
			if !target.launcher {
				namedFixture := filepath.Join(state, "codex")
				binary, err := os.ReadFile(plugin)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(namedFixture, binary, 0700); err != nil {
					t.Fatal(err)
				}
				checkProgramInference(t, c, namedFixture)
				checkTerminalMutations(t, c, namedFixture)
			}

		})
	}
}
