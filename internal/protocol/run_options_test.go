package protocol

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRunOptions(t *testing.T) {
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
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
			defer cancel()
			endpoint := startTestTarget(t, target.binary, target.native, "--socket-mode", "0660", "--websocket-origin-pattern", "*.example.com")
			c := dialCompatible(t, ctx, endpoint)
			stat, err := os.Stat(strings.TrimPrefix(endpoint, "unix://"))
			if err != nil {
				t.Fatal(err)
			}
			if stat.Mode().Perm() != 0660 {
				t.Fatalf("Unix socket mode: %o", stat.Mode().Perm())
			}
			for _, origin := range []string{"https://allowed.example.com", "https://foreign.invalid"} {
				conn, response, err := websocket.Dial(ctx, c.base+"/api/control", &websocket.DialOptions{HTTPClient: c.opts.HTTPClient, HTTPHeader: http.Header{"Origin": []string{origin}}, Subprotocols: []string{"rex.control.v1"}})
				if origin == "https://allowed.example.com" {
					if err != nil {
						t.Fatal(err)
					}
					conn.CloseNow()
				} else {
					if err == nil {
						conn.CloseNow()
						t.Fatal("unlisted origin was accepted")
					}
					if response == nil || response.StatusCode != 403 {
						t.Fatalf("origin rejection: %v %v", response, err)
					}
				}
			}
			c.conn.CloseNow()
			emptyEndpoint := startTestTarget(t, target.binary, target.native, "--exit-empty")
			active := dialCompatible(t, ctx, emptyEndpoint)
			if active.call("server.status", map[string]any{})["exit_empty"] != true {
				t.Fatal("exit-empty setting is missing from status")
			}
			sid := active.call("session.create", map[string]any{})["session_id"]
			active.conn.CloseNow()
			time.Sleep(150 * time.Millisecond)
			if _, err := os.Stat(strings.TrimPrefix(emptyEndpoint, "unix://")); err != nil {
				t.Fatalf("server stopped with a live session: %v", err)
			}
			last := dialCompatible(t, ctx, emptyEndpoint)
			last.call("session.attach", map[string]any{"session_id": sid})
			last.call("session.destroy", map[string]any{"session_id": sid})
			last.conn.CloseNow()
			time.Sleep(150 * time.Millisecond)
			if _, err := os.Stat(strings.TrimPrefix(emptyEndpoint, "unix://")); err != nil {
				t.Fatalf("server discarded the control reconnect grace: %v", err)
			}
			for deadline := time.Now().Add(65 * time.Second); ; {
				if _, err := os.Stat(strings.TrimPrefix(emptyEndpoint, "unix://")); os.IsNotExist(err) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("exit-empty server remained after its last session and connection")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
