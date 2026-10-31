package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
)

// Wire schemas for the supported Rex control and terminal APIs.
//
//go:embed help.json
var helpJSON []byte

//go:embed terminal-events.json
var terminalEventsJSON []byte

//go:embed api-guide.txt
var apiGuide []byte

var helpOnce sync.Once
var helpCatalog object
var terminalOnce sync.Once
var terminalCatalog object

func help() object {
	helpOnce.Do(func() {
		if err := json.Unmarshal(helpJSON, &helpCatalog); err != nil {
			panic(err)
		}
	})
	return helpCatalog
}

// Call with s.mu held so reload cannot change the creator catalog mid-response.
func (s *Server) help() object {
	out := cloneObject(help())
	types := append([]any{}, out["block_types"].([]any)...)
	names := []string{}
	for name := range s.creators {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		creator := s.creators[name].info
		flavors := []any{}
		for _, value := range creator["flavors"].([]any) {
			flavors = append(flavors, asObject(value)["flavor"])
		}
		methods := []any{}
		api := asObject(asObject(creator["api"])["methods"])
		methodNames := []string{}
		for method := range api {
			methodNames = append(methodNames, method)
		}
		slices.Sort(methodNames)
		for _, method := range methodNames {
			definition := asObject(api[method])
			full := name + "." + method
			record := object{"name": method, "invoke": object{"method": full}, "validation_notes": []any{fmt.Sprintf("Invoke this through `%s` and place the method payload in `args` alongside `session_id` and `block_id`.", full)}}
			for source, target := range map[string]string{"description": "description", "input_schema": "args_schema", "output_schema": "output_schema", "actions": "actions"} {
				if definition[source] != nil {
					record[target] = definition[source]
				}
			}
			methods = append(methods, record)
		}
		types = append(types, object{"creator": name, "flavors": flavors, "methods": methods})
	}
	out["block_types"] = types
	return out
}

func terminalAPI() object {
	terminalOnce.Do(func() {
		methods := object{}
		for _, kind := range help()["block_types"].([]any) {
			for _, item := range asObject(kind)["methods"].([]any) {
				m := asObject(item)
				definition := object{"description": m["description"], "input_schema": m["args_schema"], "output_schema": m["output_schema"]}
				if m["actions"] != nil {
					definition["actions"] = m["actions"]
				}
				methods[text(m, "name")] = definition
			}
		}
		events := object{}
		if err := json.Unmarshal(terminalEventsJSON, &events); err != nil {
			panic(err)
		}
		terminalCatalog = object{"methods": methods, "events": events}
	})
	return terminalCatalog
}
