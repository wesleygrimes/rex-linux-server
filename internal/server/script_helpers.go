package server

import (
	"fmt"
	"slices"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

func scriptContext(c *client, input object) object {
	context := object{"client_id": c.id, "origin": input["origin"]}
	for _, key := range []string{"session_id", "block_id", "server"} {
		if value := text(input, key); value != "" {
			context[key] = value
		}
	}
	return context
}

func (vm *scriptVM) apiNamespace(namespace, server string) *lua.LTable {
	state := vm.state
	table, meta := state.NewTable(), state.NewTable()
	meta.RawSetString("__index", state.NewFunction(func(state *lua.LState) int {
		name := namespace + "." + state.CheckString(2)
		for _, value := range help()["methods"].([]any) {
			if text(asObject(value), "name") == name {
				state.Push(state.NewFunction(func(state *lua.LState) int { return vm.apiCallTo(state, name, 1, "", server) }))
				return 1
			}
		}
		state.Push(lua.LNil)
		return 1
	}))
	state.SetMetatable(table, meta)
	return table
}

func (vm *scriptVM) luaServer(state *lua.LState) int {
	name := state.CheckString(1)
	if vm.active == nil {
		state.RaiseError("rex.server requires an active handler")
		return 0
	}
	servers, _ := vm.active.input["servers"].([]any)
	for _, value := range servers {
		if text(asObject(value), "name") == name {
			state.Push(vm.serverProxy(name))
			return 1
		}
	}
	state.RaiseError("rex.server: no server named %q: the request being handled did not list one", name)
	return 0
}

func (vm *scriptVM) installHelpers(rex *lua.LTable) {
	state := vm.state
	rex.RawSetString("servers", state.NewFunction(func(state *lua.LState) int {
		names := []any{}
		if vm.active != nil {
			servers, _ := vm.active.input["servers"].([]any)
			for _, value := range servers {
				names = append(names, text(asObject(value), "name"))
			}
		}
		state.Push(toLua(state, names))
		return 1
	}))
	rex.RawSetString("bindings", state.NewFunction(func(state *lua.LState) int {
		if vm.loaded {
			state.RaiseError("rex.bindings: bindings are listed while the file loads and cannot be listed while it runs")
			return 0
		}
		items := []any{}
		for _, binding := range vm.bindings {
			record := cloneObject(binding.record)
			key := strings.Join(stringList(record["keys"]), ">")
			if mode := text(record, "mode"); mode != "" {
				key = mode + "/" + key
			}
			delete(record, "keys")
			delete(record, "mode")
			record["key"] = key
			items = append(items, record)
		}
		state.Push(toLua(state, items))
		return 1
	}))
	rex.RawSetString("modes", state.NewFunction(func(state *lua.LState) int {
		if vm.loaded {
			state.RaiseError("rex.modes: modes are listed while the file loads and cannot be listed while it runs")
			return 0
		}
		names := []string{}
		for name := range vm.modes {
			names = append(names, name)
		}
		slices.Sort(names)
		items := []any{}
		for _, name := range names {
			items = append(items, vm.modes[name])
		}
		state.Push(toLua(state, items))
		return 1
	}))
	rex.RawSetString("invoke", state.NewFunction(vm.invoke))
	rex.RawSetString("on", state.NewFunction(func(state *lua.LState) int {
		if vm.loaded {
			vm.warn("rex.on: handlers are registered while the script loads")
			return 0
		}
		name := state.CheckString(1)
		state.CheckFunction(2)
		if name == "" || strings.Contains(name, ":") || strings.HasPrefix(name, "com.superlogical.") {
			vm.warn("event names have no creator prefix")
			return 0
		}
		vm.handlers++
		return 0
	}))
	layout := state.NewTable()
	layout.RawSetString("block", state.NewFunction(func(state *lua.LState) int {
		definition := state.CheckTable(1)
		out := state.NewTable()
		out.RawSetString("block", definition)
		state.Push(out)
		return 1
	}))
	for _, direction := range []string{"horizontal", "vertical"} {
		layout.RawSetString(direction, state.NewFunction(func(state *lua.LState) int {
			ratio := state.CheckNumber(1)
			before, after := state.CheckTable(2), state.CheckTable(3)
			if ratio <= 0 || ratio >= 1 {
				state.RaiseError("split ratio must be between zero and one")
				return 0
			}
			split := state.NewTable()
			split.RawSetString("direction", lua.LString(direction))
			split.RawSetString("ratio", ratio)
			split.RawSetString("before", before)
			split.RawSetString("after", after)
			out := state.NewTable()
			out.RawSetString("split", split)
			state.Push(out)
			return 1
		}))
	}
	rex.RawSetString("layout", layout)
}

func (vm *scriptVM) invoke(state *lua.LState) int {
	name := state.CheckString(1)
	if vm.active == nil {
		state.RaiseError("cannot invoke %q while the file loads: actions run once it has loaded", name)
		return 0
	}
	args := object{}
	if state.Get(2) != lua.LNil {
		value, err := fromLua(state.Get(2))
		if err != nil {
			state.RaiseError("%v", err)
			return 0
		}
		var ok bool
		args, ok = value.(map[string]any)
		if !ok {
			state.RaiseError("action arguments must be a table")
			return 0
		}
	}
	for _, action := range vm.actions {
		if action.record["name"] != name {
			continue
		}
		if vm.depth >= 32 {
			state.RaiseError("rex.invoke: recursion limit %d exceeded invoking %q", 32, name)
			return 0
		}
		if err := validateArgs(action.record["args"], args); err != nil {
			state.Push(lua.LNil)
			state.Push(lua.LString(err.Error()))
			return 2
		}
		context := scriptContext(vm.active.client, vm.active.input)
		vm.depth++
		defer func() { vm.depth-- }()
		if err := state.CallByParam(lua.P{Fn: action.fn, NRet: 1, Protect: true}, toLua(state, context), toLua(state, args)); err != nil {
			state.RaiseError("%v", err)
			return 0
		}
		return 1
	}
	state.Push(lua.LNil)
	state.Push(lua.LString(fmt.Sprintf("unknown action %q", name)))
	return 2
}
