package server

type blockAction struct {
	record    object
	method    string
	schema    any
	fixedArgs any
	declared  bool
}

func (s *Server) blockActions() map[string]blockAction {
	apis := map[string]object{"com.superlogical.terminal": terminalAPI()}
	for name, creator := range s.creators {
		apis[name] = asObject(creator.info["api"])
	}
	actions := map[string]blockAction{}
	for creator, api := range apis {
		for name, value := range asObject(api["methods"]) {
			method := asObject(value)
			fullName := creator + "." + name
			record := object{"name": fullName, "kind": "block"}
			if method["description"] != nil {
				record["description"] = method["description"]
			}
			if method["input_schema"] != nil {
				record["args"] = method["input_schema"]
			}
			schema := method["input_schema"]
			if schema == nil {
				schema = object{"type": "object", "additionalProperties": false}
			}
			actions[fullName] = blockAction{record: record, method: fullName, schema: schema}
		}
		for name, value := range asObject(api["methods"]) {
			method := asObject(value)
			declared, _ := method["actions"].([]any)
			for _, value := range declared {
				definition := asObject(value)
				actionName := text(definition, "name")
				if actionName == "" {
					continue
				}
				fullName := creator + "." + actionName
				record := object{"name": fullName, "kind": "block"}
				for _, field := range []string{"title", "description", "category", "keywords"} {
					if definition[field] != nil {
						record[field] = definition[field]
					}
				}
				args := definition["args"]
				actions[fullName] = blockAction{record: record, method: creator + "." + name, declared: true, fixedArgs: args, schema: object{"type": "object", "additionalProperties": false}}
			}
		}
	}
	return actions
}
