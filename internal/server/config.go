package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func defaultConfigPath() string {
	if path := os.Getenv("REX_CONFIG"); path != "" {
		return path
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "rex", "init.lua")
}

func (s *Server) configInfo() object {
	return object{"path": s.configPath, "exists": s.configExists, "generation": s.configGeneration, "instance_id": s.instanceID, "hash": s.configDigest}
}

func (s *Server) loadConfig(ctx context.Context) object {
	s.configLoad.Lock()
	defer s.configLoad.Unlock()
	data, err := os.ReadFile(s.configPath)
	exists := !os.IsNotExist(err)
	var vm *scriptVM
	digest := ""
	if err == nil {
		digest = fmt.Sprintf("%x", sha256.Sum256(data))
		vm, err = s.loadScripts(ctx, []any{object{"name": s.configPath, "source": string(data)}}, false)
		if err == nil && len(vm.modules) > 0 {
			hash := sha256.New()
			hash.Write(data)
			names := []string{}
			for name := range vm.modules {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				hash.Write([]byte(name))
				hash.Write([]byte{0})
				hash.Write(vm.modules[name])
			}
			digest = fmt.Sprintf("%x", hash.Sum(nil))
		}
	} else if !exists {
		err = nil
	}
	diagnostics := []any{}
	if err != nil {
		diagnostics = append(diagnostics, object{"severity": "error", "source": s.configPath, "message": err.Error()})
	} else if vm != nil {
		diagnostics = append(diagnostics, vm.diagnostics...)
	}
	s.mu.Lock()
	if s.closed {
		generation := s.configGeneration
		s.mu.Unlock()
		if vm != nil {
			vm.close()
		}
		return object{"diagnostics": []any{}, "generation": generation}
	}
	stored, storedErr := s.readHosts()
	previousHostsHash := hostsHash(s.mergedHosts(stored))
	s.configGeneration++
	s.configExists, s.configDigest, s.configDiagnostics = exists, digest, diagnostics
	var old *scriptVM
	if err == nil {
		old, s.configVM = s.configVM, vm
	}
	if storedErr == nil {
		if hash := hostsHash(s.mergedHosts(stored)); hash != previousHostsHash {
			s.emitLocked("hosts_changed", object{"hash": hash}, "")
		}
	}
	s.emitLocked("keymap_changed", object{"generation": s.configGeneration, "hash": s.configDigest}, "")
	result := object{"generation": s.configGeneration, "diagnostics": diagnostics}
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	return result
}

func (s *Server) scriptRequest(ctx context.Context, c *client, method string, a object) (any, error) {
	switch method {
	case "server.config_reload":
		if !c.principal.Local() {
			return nil, &apiError{"forbidden", "this method requires the local Unix socket", 403}
		}
		return s.loadConfig(ctx), nil
	case "client.set_script":
		value, present := a["scripts"]
		if !present {
			return nil, bad("scripts is required")
		}
		sources, ok := value.([]any)
		if !ok && value != nil {
			return nil, bad("scripts must be an array")
		}
		for _, value := range sources {
			source, ok := value.(map[string]any)
			if !ok {
				return nil, bad("each script must be an object")
			}
			if _, ok := source["source"].(string); !ok {
				return nil, bad("each script needs source text")
			}
		}
		var vm *scriptVM
		if len(sources) > 0 {
			var err error
			vm, err = s.loadScripts(ctx, sources, true)
			if err != nil {
				return nil, err
			}
		}
		s.mu.Lock()
		if s.closed || ctx.Err() != nil {
			s.mu.Unlock()
			if vm != nil {
				vm.close()
			}
			return nil, &apiError{"client_unavailable", "client disconnected", 503}
		}
		old := c.script
		c.script = vm
		s.mu.Unlock()
		if old != nil {
			old.close()
		}
		out := object{}
		if vm != nil && len(vm.interests) > 0 {
			out["bindings"] = vm.interests
		}
		if vm != nil && len(vm.diagnostics) > 0 {
			out["diagnostics"] = vm.diagnostics
		}
		return out, nil
	case "client.key":
		return s.scriptKey(ctx, c, a)
	case "client.action":
		return s.scriptAction(ctx, c, a)
	}
	return nil, missing("unknown script operation")
}

func (s *Server) actionInput(c *client, input object, needsSession bool) (object, error) {
	input = cloneObject(input)
	if origin := text(input, "origin"); origin == "" {
		input["origin"] = "api"
	} else if !slices.Contains([]string{"api", "key", "palette"}, origin) {
		return nil, bad("origin must be api, key or palette")
	}
	sid := text(input, "session_id")
	if text(input, "server") != "" {
		return input, nil
	}
	if sid == "" && !needsSession {
		return input, nil
	}
	v, err := s.findSession(sid)
	if err != nil {
		return nil, err
	}
	if !c.attached[sid] {
		return nil, &apiError{"conflict", "stream is not attached to session " + sid, 409}
	}
	if text(input, "block_id") == "" {
		if b, err := v.findBlock(""); err == nil {
			input["block_id"] = b.id
		}
	}
	return input, nil
}

func cloneObject(input object) object {
	copy := object{}
	for key, value := range input {
		copy[key] = value
	}
	return copy
}

func (s *Server) scriptAction(ctx context.Context, c *client, a object) (any, error) {
	if err := validateActionServers(a); err != nil {
		return nil, err
	}
	name := text(a, "action")
	if name == "" {
		return nil, bad("action is required")
	}
	s.mu.Lock()
	if action, known := s.blockActions()[name]; known {
		if action.declared && action.fixedArgs != nil && a["args"] != nil {
			s.mu.Unlock()
			return nil, bad("action " + name + " has fixed arguments; omit args")
		}
		if text(a, "server") != "" {
			s.mu.Unlock()
			return nil, bad("block actions must be sent to the server that owns the block")
		}
		input, err := s.actionInput(c, a, true)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		args := input["args"]
		if args == nil {
			args = object{}
		}
		if err := validateArgs(action.schema, args); err != nil {
			s.mu.Unlock()
			return nil, bad(err.Error())
		}
		if action.declared {
			input["args"] = action.fixedArgs
		}
		result, err := s.dispatch(c, action.method, input)
		s.mu.Unlock()
		return object{"result": result, "client_actions": []any{}}, err
	}
	vm := s.configVM
	if vm == nil {
		s.mu.Unlock()
		return nil, missing("unknown action " + name)
	}
	var action *scriptAction
	for i := range vm.actions {
		if vm.actions[i].record["name"] == name {
			action = &vm.actions[i]
			break
		}
	}
	if action == nil {
		s.mu.Unlock()
		return nil, missing("unknown action " + name)
	}
	input, err := s.actionInput(c, a, false)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	} else {
		args := a["args"]
		if args == nil {
			args = object{}
		}
		err = validateArgs(action.record["args"], args)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, bad(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	contextValue := scriptContext(c, input)
	args := input["args"]
	if args == nil {
		args = object{}
	}
	result, queue, err := vm.execute(ctx, c, input, action.fn, contextValue, args)
	if err != nil {
		return nil, err
	}
	out := object{"client_actions": queue}
	if result != nil && result != true {
		out["result"] = result
	}
	return out, nil
}

func (s *Server) scriptKey(ctx context.Context, c *client, a object) (any, error) {
	if value, present := a["prefix"]; present && value != nil {
		canonical, err := canonicalKeys(value)
		if err != nil {
			return nil, bad("prefix: " + err.Error())
		}
		a = cloneObject(a)
		a["prefix"] = canonical
	}
	s.mu.Lock()
	input, err := s.actionInput(c, a, true)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	input["origin"] = "key"
	vmList := []*scriptVM{c.script, s.configVM}
	s.mu.Unlock()
	key := asObject(a["key"])
	if text(key, "action") != "release" {
		sequence := append(stringList(a["prefix"]), logicalKey(key))
		physical := append(stringList(a["prefix"]), physicalKey(key))
		for _, vm := range vmList {
			if vm == nil {
				continue
			}
			vm.mu.Lock()
			bindings := slices.Clone(vm.bindings)
			interested := vm.keyInterests[strings.Join(sequence, ">")] || vm.keyInterests[strings.Join(physical, ">")]
			vm.mu.Unlock()
			if !interested {
				continue
			}
			for i := len(bindings) - 1; i >= 0; i-- {
				binding := bindings[i]
				if binding.fn == nil || !slices.Equal(stringList(binding.record["keys"]), sequence) && !slices.Equal(stringList(binding.record["keys"]), physical) {
					continue
				}
				ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				contextValue := scriptContext(c, input)
				result, _, err := vm.execute(ctx, c, input, binding.fn, contextValue, key)
				cancel()
				if err != nil {
					return nil, err
				}
				if result == true {
					return object{"handled": true}, nil
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return nil, &apiError{"client_unavailable", "client disconnected", 503}
	}
	return s.dispatch(c, "client.key", input)
}

func keyModifiers(key object) string {
	modifiers := strings.Split(text(key, "mods"), "+")
	result := []string{}
	for _, modifier := range []string{"ctrl", "alt", "shift", "cmd"} {
		name := modifier
		if modifier == "cmd" {
			name = "super"
		}
		if slices.Contains(modifiers, name) {
			result = append(result, modifier)
		}
	}
	if len(result) == 0 {
		return ""
	}
	return strings.Join(result, "+") + "+"
}

func logicalKey(key object) string {
	name := text(key, "key")
	if point := number(key, "unshifted_codepoint", 0); point > 0 {
		name = string(rune(point))
	} else if strings.HasPrefix(name, "key_") {
		name = strings.TrimPrefix(name, "key_")
	} else if strings.HasPrefix(name, "arrow_") {
		name = strings.TrimPrefix(name, "arrow_")
	} else if text(key, "text") != "" && text(key, "text") != "\r" {
		name = text(key, "text")
	}
	value := keyModifiers(key) + name
	if canonical, err := canonicalBinding(value); err == nil {
		return canonical
	}
	return value
}

func physicalKey(key object) string {
	parts := strings.Split(text(key, "key"), "_")
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return keyModifiers(key) + "[" + strings.Join(parts, "") + "]"
}
