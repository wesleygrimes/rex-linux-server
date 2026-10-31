package server

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/google/jsonschema-go/jsonschema"
	lua "github.com/yuin/gopher-lua"
)

func validateArgs(schema, value any) error {
	if schema == nil {
		return nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var definition jsonschema.Schema
	if err := json.Unmarshal(encoded, &definition); err != nil {
		return err
	}
	resolved, err := definition.Resolve(nil)
	if err != nil {
		return err
	}
	return resolved.Validate(value)
}

func toLua(state *lua.LState, value any) lua.LValue {
	switch value := value.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(value)
	case string:
		return lua.LString(value)
	case float64:
		return lua.LNumber(value)
	case int:
		return lua.LNumber(value)
	case object:
		table := state.NewTable()
		for key, item := range value {
			table.RawSetString(key, toLua(state, item))
		}
		return table
	case []any:
		table := state.NewTable()
		for _, item := range value {
			table.Append(toLua(state, item))
		}
		return table
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return lua.LNil
		}
		var generic any
		if json.Unmarshal(encoded, &generic) != nil {
			return lua.LNil
		}
		return toLua(state, generic)
	}
}

func fromLua(value lua.LValue) (any, error) {
	return readLuaValue(value, map[*lua.LTable]bool{}, 0)
}

func readLuaValue(value lua.LValue, seen map[*lua.LTable]bool, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Lua value exceeds maximum nesting depth")
	}
	switch value := value.(type) {
	case *lua.LNilType:
		return nil, nil
	case lua.LBool:
		return bool(value), nil
	case lua.LString:
		return string(value), nil
	case lua.LNumber:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("number is not valid JSON")
		}
		return float64(value), nil
	case *lua.LTable:
		if seen[value] {
			return nil, fmt.Errorf("cyclic Lua table")
		}
		seen[value] = true
		defer delete(seen, value)
		length, count, array := value.Len(), 0, true
		value.ForEach(func(key, _ lua.LValue) {
			count++
			number, ok := key.(lua.LNumber)
			if !ok || int(number) < 1 || int(number) > length || float64(number) != float64(int(number)) {
				array = false
			}
		})
		if array && count > 0 && count == length {
			items := make([]any, length)
			for i := range items {
				item, err := readLuaValue(value.RawGetInt(i+1), seen, depth+1)
				if err != nil {
					return nil, err
				}
				items[i] = item
			}
			return items, nil
		}
		out := object{}
		var failure error
		value.ForEach(func(key, item lua.LValue) {
			if failure != nil {
				return
			}
			name, ok := key.(lua.LString)
			if !ok {
				failure = fmt.Errorf("object table keys must be strings")
				return
			}
			out[string(name)], failure = readLuaValue(item, seen, depth+1)
		})
		return out, failure
	default:
		return nil, fmt.Errorf("cannot encode Lua %s as JSON", value.Type())
	}
}
