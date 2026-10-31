package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type object = map[string]any

var version = "initial"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "terminal" {
		terminalFixture()
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fd, err := strconv.Atoi(os.Getenv("REX_BLOCKSERVER_INFO_FD"))
	if err != nil {
		panic(err)
	}
	info := os.NewFile(uintptr(fd), "connection-info")
	json.NewEncoder(info).Encode(object{"pid": os.Getpid(), "network": "tcp", "addr": listener.Addr().String(), "base_url": "http://" + listener.Addr().String()})
	info.Close()
	var mu sync.Mutex
	next := 0
	blocks := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/control", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"rex.control.v1"}})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := context.Background()
		var hello object
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, object{"type": "welcome", "protocol": "rex.control.v1", "limits": object{"max_message_bytes": 1048576}, "client": object{"id": "client:00000000-0000-4000-8000-000000000001", "reconnect_token": "plugin-test"}}); err != nil {
			return
		}
		for {
			var req object
			if err := wsjson.Read(ctx, conn, &req); err != nil {
				return
			}
			payload, _ := req["payload"].(map[string]any)
			if path := os.Getenv("REX_TEST_PLUGIN_LOG"); path != "" {
				f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if f != nil {
					json.NewEncoder(f).Encode(req)
					f.Close()
				}
			}
			result := object{}
			var failure object
			mu.Lock()
			switch req["method"] {
			case "block_creator.list":
				result["creators"] = []any{object{"name": "dev.example.echo", "title": "Echo", "description": "Protocol test block.", "flavors": []any{object{"name": "echo", "flavor": "dev.example.echo.echo", "title": "Echo", "description": "Echo test messages.", "options_schema": object{"type": "object"}}}, "api": object{"methods": object{"mirror": object{"description": "Return any JSON input.", "input_schema": true, "output_schema": object{"type": "object"}}, "echo": object{"description": "Return the supplied message.", "input_schema": object{"type": "object"}, "output_schema": object{"type": "object"}, "actions": []any{object{"name": "greet", "title": "Greeting", "args": object{"message": "fixed greeting"}}}}}, "events": object{"echoed": object{"type": "object"}}}}}
			case "block.create":
				next++
				bid, _ := payload["block_id"].(string)
				if bid == "" {
					bid = fmt.Sprintf("block:00000000-0000-4000-8000-%012d", next)
				}
				blocks[bid] = true
				result["block_id"] = bid
			case "block.method":
				if !blocks[fmt.Sprint(payload["block_id"])] {
					failure = object{"code": "not_found", "message": "block not found", "status": 404}
					break
				}
				if payload["method"] == "mirror" {
					result = object{"value": payload["args"]}
					break
				}
				args, _ := payload["args"].(map[string]any)
				result = object{"message": args["message"], "version": version}
			case "block.close":
				delete(blocks, fmt.Sprint(payload["block_id"]))
			case "block.event":
				event, _ := payload["event"].(map[string]any)
				if event["type"] == "paste" {
					input, _ := event["payload"].(map[string]any)
					wsjson.Write(ctx, conn, object{"type": "event", "seq": 1, "payload": object{"type": "block_event", "payload": object{"block_id": payload["block_id"], "name": "echoed", "payload": object{"message": input["text"]}}}})
				}
			default:
				failure = object{"code": "method_not_found", "message": "unknown plugin method", "status": 404}
			}
			mu.Unlock()
			response := object{"type": "response", "id": req["id"], "seq": 0}
			if failure != nil {
				response["error"] = failure
			} else {
				response["payload"] = result
			}
			if err := wsjson.Write(ctx, conn, response); err != nil {
				return
			}
		}
	})
	if err := http.Serve(listener, mux); err != nil {
		panic(err)
	}
}
