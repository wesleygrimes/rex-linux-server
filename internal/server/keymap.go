package server

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var modeName = regexp.MustCompile(`^[a-z0-9_-]+$`)

func (s *Server) keymapRequest(c *client, a object) (object, error) {
	a = cloneObject(a)
	if scope := text(a, "server_actions"); scope != "" && !slices.Contains([]string{"all", "configured", "none"}, scope) {
		return nil, bad("server_actions must be all, configured or none")
	}
	names := map[string]bool{}
	for _, kind := range []string{"defaults", "overrides"} {
		layers, ok := a[kind].([]any)
		if !ok && a[kind] != nil {
			return nil, bad(kind + " must be an array")
		}
		for _, value := range layers {
			layer := asObject(value)
			name := text(layer, "name")
			if name == "" || strings.Contains(name, ":") || names[name] {
				return nil, bad("keymap layer names must be non-empty, unique and contain no colon")
			}
			names[name] = true
			for _, group := range []string{"bindings", "removals"} {
				items, ok := layer[group].([]any)
				if !ok && layer[group] != nil {
					return nil, bad(group + " must be an array")
				}
				for _, value := range items {
					item := asObject(value)
					mode := text(item, "mode")
					if mode != "" && (mode == "root" || !modeName.MatchString(mode)) {
						return nil, bad("invalid keymap mode " + mode)
					}
					keys, _ := item["keys"].([]any)
					if item["keys"] != nil {
						if _, ok := item["keys"].([]any); !ok {
							return nil, bad("keys must be an array")
						}
						for _, value := range keys {
							_, ok := value.(string)
							if !ok {
								return nil, bad("keys must contain strings")
							}
						}
						if canonical, err := canonicalKeys(keys); err == nil {
							item["keys"] = canonical
						}
					}
					if len(keys) == 0 && (mode == "" || group == "bindings" || text(item, "action") != "") {
						return nil, bad("binding needs keys; a whole-mode removal cannot name an action")
					}
					if group == "bindings" && text(item, "action") == "" {
						return nil, bad("binding needs an action")
					}
				}
			}
		}
	}
	input := cloneObject(a)
	defaults, _ := a["defaults"].([]any)
	defaults = slices.Clone(defaults)
	for _, vm := range []*scriptVM{s.configVM, c.script} {
		if vm != nil {
			defaults = append(defaults, vm.layers...)
		}
	}
	input["defaults"] = defaults
	out := s.keymap(input)
	mergeDiagnostics, _ := out["diagnostics"].([]any)
	diagnostics := append(slices.Clone(s.configDiagnostics), mergeDiagnostics...)
	if diagnostics == nil {
		diagnostics = []any{}
	}
	if c.script != nil {
		diagnostics = append(diagnostics, c.script.diagnostics...)
	}
	actions, _ := out["actions"].([]any)
	if s.configVM != nil && text(a, "server_actions") != "none" {
		configured := []any{}
		for _, action := range s.configVM.actions {
			record := cloneObject(action.record)
			record["source"] = filepath.Base(text(record, "source"))
			configured = append(configured, record)
		}
		actions = append(configured, actions...)
	}
	if len(actions) > 0 {
		out["actions"] = actions
	}
	known := map[string]object{}
	for _, vm := range []*scriptVM{s.configVM} {
		if vm != nil {
			for _, action := range vm.actions {
				known[text(action.record, "name")] = action.record
			}
		}
	}

	declared, _ := a["actions"].([]any)
	checkUnknown := len(declared) > 0
	if checkUnknown {
		for _, value := range declared {
			action := asObject(value)
			known[text(action, "name")] = action
		}
	}
	for name, action := range s.blockActions() {
		known[name] = object{"args": action.schema, "kind": "block"}
	}
	bindings := out["bindings"].([]any)
	for _, value := range bindings {
		binding := asObject(value)
		name := text(binding, "action")
		source := text(binding, "source")
		if strings.Contains(source, ":") {
			source = filepath.Base(source)
			binding["source"] = source
		}
		defaultSource := false
		if layers, ok := a["defaults"].([]any); ok {
			for _, layer := range layers {
				defaultSource = defaultSource || source == text(asObject(layer), "name")
			}
		}
		if defaultSource || name == "client.key.forward" {
			continue
		}
		message := ""
		if action, exists := known[name]; exists {
			args := binding["args"]
			if args == nil {
				args = object{}
			}
			if err := validateArgs(action["args"], args); err != nil && (action["kind"] == "lua" || action["kind"] == "block") {
				message = fmt.Sprintf("invalid arguments for %s: %v", name, err)
			}
		} else if checkUnknown {
			message = "unknown action " + name
		}
		if message != "" {
			diagnostic := object{"severity": "warning", "source": source, "message": message, "action": name, "key": strings.Join(stringList(binding["keys"]), " ")}
			if mode := text(binding, "mode"); mode != "" {
				diagnostic["mode"] = mode
			}
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	diagnostics = append(diagnostics, keymapModeWarnings(out, s.configVM, c.script)...)
	diagnostics = append(diagnostics, keymapShadowWarnings(out, a)...)
	out["diagnostics"] = diagnostics
	if _, exists := a["actions"]; exists {
		c.actions = declared
	}
	return out, nil
}
