package server

import (
	"fmt"
	"path/filepath"
	"strings"
)

func bindingWarning(binding object, message string) object {
	warning := object{"severity": "warning", "source": binding["source"], "key": strings.Join(stringList(binding["keys"]), " "), "message": message}
	if action := text(binding, "action"); action != "" && action != "client.key.forward" {
		warning["action"] = action
	}
	if mode := text(binding, "mode"); mode != "" {
		warning["mode"] = mode
	}
	return warning
}

func keymapModeWarnings(out object, vms ...*scriptVM) []any {
	warnings := []any{}
	modes := map[string]bool{}
	if items, ok := out["modes"].([]any); ok {
		for _, value := range items {
			modes[text(asObject(value), "name")] = true
		}
	}
	bound := map[string]bool{}
	for _, value := range out["bindings"].([]any) {
		binding := asObject(value)
		bound[text(binding, "mode")] = true
	}
	for _, vm := range vms {
		if vm == nil {
			continue
		}
		for name, record := range vm.modes {
			if modes[name] && !bound[name] {
				warnings = append(warnings, object{"severity": "warning", "source": filepath.Base(text(record, "source")), "mode": name, "message": fmt.Sprintf("mode %q is declared but nothing is bound in it", name)})
			}
		}
	}
	return warnings
}

func keymapShadowWarnings(out, input object) []any {
	warnings := []any{}
	defaults := map[string]bool{}
	if layers, ok := input["defaults"].([]any); ok {
		for _, layer := range layers {
			defaults[text(asObject(layer), "name")] = true
		}
	}
	top := map[string]object{}
	reported := map[string]bool{}
	modes := map[string]bool{}
	if items, ok := out["modes"].([]any); ok {
		for _, value := range items {
			modes[text(asObject(value), "name")] = true
		}
	}
	for _, value := range out["bindings"].([]any) {
		binding := asObject(value)
		if text(binding, "action") == "client.mode.enter" && !modes[text(asObject(binding["args"]), "name")] && !defaults[text(binding, "source")] {
			warnings = append(warnings, bindingWarning(binding, "binding enters an undefined mode"))
		}
		keys := stringList(binding["keys"])
		if len(keys) == 0 {
			continue
		}
		if _, err := canonicalKeys(binding["keys"]); err != nil {
			continue
		}
		index := text(binding, "mode") + ":" + keys[0]
		above := top[index]
		if above == nil {
			top[index] = binding
			continue
		}
		aboveKeys := stringList(above["keys"])
		if (len(keys) == 1) != (len(aboveKeys) == 1) && !reported[index] && !defaults[text(above, "source")] {
			warnings = append(warnings, bindingWarning(binding, fmt.Sprintf("binding cannot run because %s at %s takes this key first", strings.Join(aboveKeys, " "), text(above, "source"))))
			reported[index] = true
		}
	}
	return warnings
}
