package server

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
)

type scriptBinding struct {
	record object
	fn     *lua.LFunction
}

type scriptAction struct {
	record object
	fn     *lua.LFunction
}

type scriptInvocation struct {
	ctx    context.Context
	client *client
	input  object
	queue  []any
}

type scriptVM struct {
	mu           sync.Mutex
	s            *Server
	state        *lua.LState
	root         context.Context
	cancel       context.CancelFunc
	bindings     []scriptBinding
	layers       []any
	removals     []object
	modes        map[string]object
	actions      []scriptAction
	hosts        []object
	diagnostics  []any
	interests    []string
	active       *scriptInvocation
	loaded       bool
	clientOnly   bool
	closed       bool
	modules      map[string][]byte
	depth        int
	handlers     int
	keyInterests map[string]bool
}

func (s *Server) loadScripts(ctx context.Context, sources []any, clientOnly bool) (*scriptVM, error) {
	root, cancel := context.WithCancel(context.Background())
	vm := &scriptVM{s: s, state: lua.NewState(lua.Options{SkipOpenLibs: clientOnly}), root: root, cancel: cancel,
		modes: map[string]object{}, diagnostics: []any{}, interests: []string{}, modules: map[string][]byte{}, clientOnly: clientOnly}
	state := vm.state
	if clientOnly {
		for _, library := range []struct {
			name string
			open lua.LGFunction
		}{{lua.BaseLibName, lua.OpenBase}, {lua.TabLibName, lua.OpenTable}, {lua.StringLibName, lua.OpenString}, {lua.MathLibName, lua.OpenMath}} {
			state.Push(state.NewFunction(library.open))
			state.Push(lua.LString(library.name))
			state.Call(1, 0)
		}
		for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "collectgarbage", "print", "require", "module", "newproxy", "_printregs"} {
			state.SetGlobal(name, lua.LNil)
		}
	} else {
		packageTable := state.GetGlobal("package").(*lua.LTable)
		dir := filepath.Dir(s.configPath)
		packageTable.RawSetString("path", lua.LString(filepath.Join(dir, "?.lua")+";"+filepath.Join(dir, "?", "init.lua")+";"+lua.LVAsString(packageTable.RawGetString("path"))))
		packageTable.RawGetString("loaders").(*lua.LTable).RawSetInt(2, state.NewFunction(vm.loadModule))
	}
	vm.install()
	loadCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	state.SetContext(loadCtx)
	defer state.RemoveContext()
	for i, value := range sources {
		source := asObject(value)
		name := text(source, "name")
		if name == "" {
			name = fmt.Sprintf("script-%d.lua", i+1)
		}
		function, err := state.Load(strings.NewReader(text(source, "source")), "@"+name)
		if err == nil {
			err = state.CallByParam(lua.P{Fn: function, NRet: 0, Protect: true})
		}
		if err != nil {
			vm.close()
			return nil, bad(shortLuaError(err))
		}
	}
	vm.loaded = true
	vm.keyInterests = map[string]bool{}
	if vm.handlers > 0 && !vm.clientOnly {
		vm.diagnostics = append(vm.diagnostics, object{"severity": "warning", "message": fmt.Sprintf("%d rex.on handlers here do nothing. Handlers in init.lua run from a later version of Rex", vm.handlers)})
	}
	for _, binding := range vm.bindings {
		if binding.fn != nil {
			vm.keyInterests[strings.Join(stringList(binding.record["keys"]), ">")] = true
		}
		if binding.fn != nil && len(stringList(binding.record["keys"])) == 1 {
			key := strings.Join(stringList(binding.record["keys"]), ">")
			if !slices.Contains(vm.interests, key) {
				vm.interests = append(vm.interests, key)
			}
		}
	}
	slices.Sort(vm.interests)
	return vm, nil
}

func (vm *scriptVM) close() {
	vm.cancel()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if !vm.closed {
		vm.closed = true
		vm.state.Close()
	}
}

func shortLuaError(err error) string {
	message, _, _ := strings.Cut(err.Error(), "\nstack traceback:")
	return message
}

func stringList(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func (vm *scriptVM) source() string {
	return strings.TrimSuffix(strings.TrimSpace(vm.state.Where(1)), ":")
}

func (vm *scriptVM) warn(message string) {
	if vm.loaded {
		log.Printf("Lua: %s: %s", vm.source(), message)
		return
	}
	vm.diagnostics = append(vm.diagnostics, object{"severity": "warning", "source": vm.source(), "message": message})
}

func (vm *scriptVM) install() {
	state := vm.state
	rex := state.NewTable()
	state.SetGlobal("rex", rex)
	state.SetFuncs(rex, map[string]lua.LGFunction{
		"bind": vm.bind, "unbind": vm.unbind, "mode": vm.mode, "action": vm.action,
		"host": vm.declareHost,
		"call": func(state *lua.LState) int { return vm.apiCall(state, state.CheckString(1), 2, "") },
		"log":  func(state *lua.LState) int { log.Printf("Lua: %s", state.Get(1)); return 0 },
	})
	rex.RawSetString("session", vm.apiNamespace("session", ""))
	clientTable := state.NewTable()
	rex.RawSetString("client", clientTable)

	clientAPI := rex.RawGetString("client").(*lua.LTable)
	clientAPI.RawSetString("queue", state.NewFunction(func(state *lua.LState) int {
		if vm.active == nil {
			state.RaiseError("rex.client.queue requires an active handler")
			return 0
		}
		queued := object{"action": state.CheckString(1)}
		if state.Get(2) != lua.LNil {
			args, err := fromLua(state.Get(2))
			if err != nil {
				state.RaiseError("%v", err)
				return 0
			}
			queued["args"] = args
		}
		vm.active.queue = append(vm.active.queue, queued)
		return 0
	}))
	block := state.NewTable()
	block.RawSetString("call", state.NewFunction(func(state *lua.LState) int {
		creator, method, index := state.CheckString(1), state.CheckString(2), 3
		server := ""
		if vm.active != nil {
			server = text(vm.active.input, "server")
		}
		return vm.apiCallTo(state, creator+"."+method, index, "", server, method)
	}))
	rex.RawSetString("block", block)
	rex.RawSetString("server", state.NewFunction(vm.luaServer))
	vm.installHelpers(rex)

}

func (vm *scriptVM) apiCall(state *lua.LState, method string, index int, blockID string) int {
	server := ""
	if vm.active != nil {
		server = text(vm.active.input, "server")
	}
	return vm.apiCallTo(state, method, index, blockID, server)
}

func (vm *scriptVM) apiCallTo(state *lua.LState, method string, index int, blockID, server string, blockMethod ...string) int {
	if vm.active == nil {
		state.Push(lua.LNil)
		state.Push(lua.LString("API calls require an active handler"))
		return 2
	}
	args := object{}
	if state.Get(index) != lua.LNil {
		value, err := fromLua(state.Get(index))
		if err != nil {
			state.RaiseError("%v", err)
			return 0
		}
		var ok bool
		args, ok = value.(map[string]any)
		if !ok {
			state.RaiseError("API arguments must be a table")
			return 0
		}
	}
	if _, present := args["session_id"]; !present && strings.HasPrefix(method, "session.") && method != "session.create" && method != "session.list" {
		if sid := text(vm.active.input, "session_id"); sid != "" {
			args["session_id"] = sid
		}
	}
	if len(blockMethod) > 0 || strings.HasPrefix(method, "com.superlogical.") {
		args = object{"session_id": vm.active.input["session_id"], "block_id": vm.active.input["block_id"], "args": args}
		if blockID != "" {
			args["block_id"] = blockID
		}
	}
	var result any
	var err error
	if server == "" {
		vm.s.mu.Lock()
		if vm.s.closed {
			err = fmt.Errorf("server stopped")
		} else {
			result, err = vm.s.dispatch(vm.active.client, method, args)
		}
		vm.s.mu.Unlock()
	} else {
		serverID := ""
		servers, _ := vm.active.input["servers"].([]any)
		for _, value := range servers {
			if item := asObject(value); text(item, "name") == server {
				serverID = text(item, "id")
			}
		}
		if serverID == "" {
			err = fmt.Errorf("unknown server %q", server)
		} else {
			result, err = vm.s.callOtherServer(vm.active.ctx, vm.active.client, serverID, method, args)
		}
	}
	if err != nil {
		state.Push(lua.LNil)
		state.Push(lua.LString(err.Error()))
		return 2
	}
	state.Push(toLua(state, result))
	return 1
}

func (vm *scriptVM) bind(state *lua.LState) int {
	key, ok := state.Get(1).(lua.LString)
	if !ok || string(key) == "" {
		vm.warn("rex.bind needs a key string")
		return 0
	}
	mode, keys := "", string(key)
	if before, after, found := strings.Cut(keys, "/"); found && before != "" && modeName.MatchString(before) {
		mode, keys = before, after
	}
	record := object{"keys": toAnyStrings(strings.Split(keys, ">")), "source": vm.source()}
	canonical, err := canonicalKeys(record["keys"])
	if err != nil {
		vm.warn(err.Error())
		return 0
	}
	record["keys"] = canonical
	if mode != "" {
		record["mode"] = mode
	}
	binding := scriptBinding{record: record}
	switch target := state.Get(2).(type) {
	case *lua.LFunction:
		if mode != "" {
			vm.warn("Lua key handlers cannot be bound in a mode")
			return 0
		}
		binding.fn = target
		record["action"] = "client.key.forward"
	case lua.LString:
		record["action"] = string(target)
		if state.Get(3) != lua.LNil {
			args, err := fromLua(state.Get(3))
			if err != nil {
				vm.warn(err.Error())
				return 0
			}
			record["args"] = args
		}
	default:
		vm.warn("rex.bind target must be an action name or function")
		return 0
	}
	vm.bindings = append(vm.bindings, binding)
	if !vm.loaded {
		vm.layers = append(vm.layers, object{"name": record["source"], "bindings": []any{record}})
	}
	return 0
}

func toAnyStrings(items []string) []any {
	result := make([]any, len(items))
	for i, value := range items {
		result[i] = value
	}
	return result
}

func (vm *scriptVM) unbind(state *lua.LState) int {
	record := object{"source": vm.source()}
	if table, ok := state.Get(1).(*lua.LTable); ok {
		value, err := fromLua(table)
		if err != nil {
			vm.warn(err.Error())
			return 0
		}
		maps.Copy(record, asObject(value))
	} else {
		key := state.CheckString(1)
		if mode, keys, found := strings.Cut(key, "/"); found {
			record["mode"], key = mode, keys
		}
		record["keys"] = toAnyStrings(strings.Split(key, ">"))
		if action := state.OptString(2, ""); action != "" {
			record["action"] = action
		}
	}
	vm.removals = append(vm.removals, record)
	if vm.loaded {
		vm.bindings = slices.DeleteFunc(vm.bindings, func(binding scriptBinding) bool {
			return text(record, "mode") == text(binding.record, "mode") && slices.Equal(stringList(record["keys"]), stringList(binding.record["keys"])) && (text(record, "action") == "" || record["action"] == binding.record["action"])
		})
	}
	if !vm.loaded {
		vm.layers = append(vm.layers, object{"name": record["source"], "removals": []any{record}})
	}
	return 0
}

func (vm *scriptVM) mode(state *lua.LState) int {
	if vm.loaded {
		state.RaiseError("rex.mode is only available while loading scripts")
		return 0
	}
	name := state.CheckString(1)
	if name == "root" || !modeName.MatchString(name) {
		vm.warn("invalid mode name " + name)
		return 0
	}
	mode := object{"name": name, "exclusive": false, "source": vm.source()}
	if table, ok := state.Get(2).(*lua.LTable); ok {
		mode["exclusive"] = lua.LVAsBool(table.RawGetString("exclusive"))
	}
	vm.modes[name] = mode
	if !vm.loaded {
		vm.layers = append(vm.layers, object{"name": mode["source"], "modes": []any{object{"name": name, "exclusive": mode["exclusive"]}}})
	}
	return 0
}

func (vm *scriptVM) action(state *lua.LState) int {
	if vm.loaded {
		state.RaiseError("rex.action is only available while loading configuration")
		return 0
	}
	if vm.clientOnly {
		state.RaiseError("rex.action is only available in the account configuration")
		return 0
	}
	definition := state.CheckTable(1)
	name := lua.LVAsString(definition.RawGetString("name"))
	title := lua.LVAsString(definition.RawGetString("title"))
	handler, ok := definition.RawGetString("run").(*lua.LFunction)
	if name == "" || title == "" || !ok {
		state.RaiseError("rex.action needs a name, title and run function")
		return 0
	}
	record := object{"name": name, "kind": "lua", "category": "Actions", "source": vm.source()}
	for _, field := range []string{"title", "description", "category", "keywords", "args"} {
		if value := definition.RawGetString(field); value != lua.LNil {
			converted, err := fromLua(value)
			if err != nil {
				state.RaiseError("%v", err)
				return 0
			}
			record[field] = converted
		}
	}
	vm.actions = append(vm.actions, scriptAction{record: record, fn: handler})
	return 0
}

func (vm *scriptVM) loadModule(state *lua.LState) int {
	name := state.CheckString(1)
	path, ok := state.GetField(state.GetGlobal("package"), "path").(lua.LString)
	if !ok {
		state.RaiseError("package.path must be a string")
		return 0
	}
	errors := []string{}
	for _, pattern := range strings.Split(string(path), ";") {
		filename := strings.ReplaceAll(pattern, "?", strings.ReplaceAll(name, ".", string(os.PathSeparator)))
		data, err := os.ReadFile(filename)
		if err != nil {
			errors = append(errors, "no file '"+filename+"'")
			continue
		}
		function, err := state.Load(strings.NewReader(string(data)), "@"+filename)
		if err != nil {
			state.RaiseError("%v", err)
			return 0
		}
		vm.modules[filename] = data
		state.Push(function)
		return 1
	}
	state.Push(lua.LString(strings.Join(errors, "\n")))
	return 1
}

func (vm *scriptVM) execute(ctx context.Context, c *client, input object, function *lua.LFunction, arguments ...any) (any, []any, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.closed || vm.root.Err() != nil {
		return nil, nil, &apiError{"conflict", "script was replaced", 409}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(vm.root, cancel)
	defer stop()
	vm.active = &scriptInvocation{ctx: ctx, client: c, input: input, queue: []any{}}
	defer func() { vm.active = nil }()
	state := vm.state
	state.SetContext(ctx)
	defer state.RemoveContext()
	luaArguments := make([]lua.LValue, len(arguments))
	for i, value := range arguments {
		luaArguments[i] = toLua(state, value)
	}
	err := state.CallByParam(lua.P{Fn: function, NRet: 1, Protect: true}, luaArguments...)
	if err != nil {
		if vm.root.Err() != nil {
			return nil, nil, &apiError{"conflict", "script was replaced", 409}
		}
		if ctx.Err() != nil {
			return nil, nil, &apiError{"timeout", "script execution timed out", 504}
		}
		return nil, nil, &apiError{"internal", shortLuaError(err), 500}
	}
	result, err := fromLua(state.Get(-1))
	state.Pop(1)
	return result, vm.active.queue, err
}
