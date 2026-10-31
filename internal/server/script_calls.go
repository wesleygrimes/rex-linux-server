package server

import (
	"context"
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"
)

func validateActionServers(a object) error {
	servers, ok := a["servers"].([]any)
	if !ok && a["servers"] != nil {
		return bad("servers must be an array")
	}
	seen := map[string]bool{}
	for _, value := range servers {
		server := asObject(value)
		name := text(server, "name")
		if name == "" || text(server, "id") == "" || seen[name] {
			return bad("servers needs unique non-empty names and non-empty IDs")
		}
		seen[name] = true
	}
	if name := text(a, "server"); name != "" && !seen[name] {
		return bad("server must name a server in servers")
	}
	return nil
}

func (s *Server) callOtherServer(ctx context.Context, c *client, serverID, method string, args object) (any, error) {
	callID := secret()
	call := &clientCall{owner: c, reply: make(chan clientReply, 1)}
	s.mu.Lock()
	if s.closed || c.conn == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("client disconnected")
	}
	s.pendingCalls[callID] = call
	s.emitClientLocked(c, "client_call_requested", object{"call_id": callID, "server": serverID, "method": method, "args": args})
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingCalls, callID)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case reply := <-call.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (vm *scriptVM) serverProxy(name string) *lua.LTable {
	state := vm.state
	proxy := state.NewTable()
	proxy.RawSetString("call", state.NewFunction(func(state *lua.LState) int {
		return vm.apiCallTo(state, state.CheckString(1), 2, "", name)
	}))
	proxy.RawSetString("session", vm.apiNamespace("session", name))

	block := state.NewTable()
	block.RawSetString("call", state.NewFunction(func(state *lua.LState) int {
		creator, method, index := state.CheckString(1), state.CheckString(2), 3
		return vm.apiCallTo(state, creator+"."+method, index, "", name, method)
	}))
	proxy.RawSetString("block", block)
	return proxy
}
