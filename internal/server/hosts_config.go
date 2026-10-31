package server

import (
	"crypto/sha1"
	"fmt"
	"slices"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

func (s *Server) mergedHosts(stored storedHosts) []object {
	hosts := slices.Clone(stored.Records)
	if hosts == nil {
		hosts = []object{}
	}
	if s.configVM != nil {
		for _, declared := range s.configVM.hosts {
			if slices.Contains(stored.Removed, text(declared, "id")) {
				continue
			}
			included := true
			for _, host := range hosts {
				if host["id"] == declared["id"] || strings.EqualFold(text(host, "label"), text(declared, "label")) {
					included = false
					break
				}
			}
			if included {
				hosts = append(hosts, declared)
			}
		}
	}
	slices.SortFunc(hosts, func(a, b object) int {
		if order := strings.Compare(strings.ToLower(text(a, "label")), strings.ToLower(text(b, "label"))); order != 0 {
			return order
		}
		return strings.Compare(text(a, "id"), text(b, "id"))
	})
	return hosts
}

func (vm *scriptVM) declareHost(state *lua.LState) int {
	if vm.clientOnly || vm.loaded {
		state.RaiseError("rex.host is only available while loading the account configuration")
		return 0
	}
	host := object{}
	if table, ok := state.Get(1).(*lua.LTable); ok {
		value, err := fromLua(table)
		if err != nil {
			vm.warn(err.Error())
			return 0
		}
		host = asObject(value)
	} else {
		host["label"] = state.CheckString(1)
		if endpoint, ok := state.Get(2).(lua.LString); ok {
			host["endpoints"] = []any{string(endpoint)}
		} else if value, err := fromLua(state.Get(2)); err == nil {
			host["endpoints"] = value
		}
	}
	if err := validateHost(host); err != nil {
		vm.warn(err.Error())
		return 0
	}
	if host["id"] == nil {
		digest := sha1.Sum([]byte("rex-host:" + text(host, "label")))
		digest[6] = digest[6]&15 | 80
		digest[8] = digest[8]&63 | 128
		host["id"] = fmt.Sprintf("host:%x-%x-%x-%x-%x", digest[:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
	}
	host["source"] = vm.source()
	vm.hosts = append(vm.hosts, host)
	return 0
}
