package server

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func matchTargets(query string, pool []object) []object {
	query = strings.TrimSpace(query)
	found, best := []object{}, 5
	fullKind, _, _ := strings.Cut(query, ":")
	fullID := slices.Contains([]string{"session", "window", "block", "client"}, fullKind) && validTypedID(query, fullKind)
	for _, candidate := range pool {
		kind := text(candidate, "kind")
		targetID := text(candidate, kind+"_id")
		rank := 5
		switch {
		case fullID:
			if query == targetID {
				rank = 0
			}
		case query != "" && strings.EqualFold(query, text(candidate, "label")):
			rank = 1
		case kind == "window" && query == fmt.Sprint(candidate["ordinal"]):
			rank = 2
		case validIDSuffix(query) && strings.HasSuffix(strings.ReplaceAll(targetID, "-", ""), strings.ToLower(query)):
			rank = 3
		}
		if rank < best {
			found, best = nil, rank
		}
		if rank == best && rank < 5 {
			found = append(found, candidate)
		}
	}
	return found
}

func sessionTarget(v *session) object {
	return object{"kind": "session", "session_id": v.id, "label": v.label}
}
func windowTarget(v *session, w *window, ordinal int) object {
	return object{"kind": "window", "session_id": v.id, "window_id": w.id, "label": w.label, "ordinal": ordinal}
}

func (s *Server) targetPool(a object) ([]object, []object, bool) {
	kinds := stringList(a["kinds"])
	wants := func(kind string) bool { return slices.Contains(kinds, kind) }
	scope, context := asObject(a["scope"]), asObject(a["context"])
	sessions := s.sessions
	sessionPool := []object{}
	for _, v := range s.sessions {
		sessionPool = append(sessionPool, sessionTarget(v))
	}
	if query, present := scope["session"]; present {
		var matches []object
		if query == "" {
			matches = defaultSession(sessionPool, text(context, "session_id"))
		} else {
			matches = matchTargets(fmt.Sprint(query), sessionPool)
		}
		if len(matches) != 1 {
			return nil, matches, true
		}
		v, _ := s.findSession(text(matches[0], "session_id"))
		sessions = []*session{v}
	} else if !wants("session") && !wants("client") {
		if v, err := s.findSession(text(context, "session_id")); err == nil {
			sessions = []*session{v}
		}
	}
	if wants("client") {
		pool := []object{}
		for _, c := range s.clients {
			if c.conn == nil {
				continue
			}
			if _, scoped := scope["session"]; scoped && !c.attached[sessions[0].id] {
				continue
			}
			pool = append(pool, object{"kind": "client", "client_id": c.id, "label": text(c.info, "label"), "where": strings.TrimSpace(text(c.info, "name") + " " + text(c.info, "version"))})
		}
		slices.SortFunc(pool, func(a, b object) int { return strings.Compare(text(a, "client_id"), text(b, "client_id")) })
		return pool, nil, false
	}
	windowID := ""
	if query, present := scope["window"]; present {
		pool := []object{}
		for _, v := range sessions {
			for j, w := range v.windows {
				pool = append(pool, windowTarget(v, w, j+1))
			}
		}
		var matches []object
		if query == "" {
			matches = s.defaultWindows(sessions, context, "")
		} else {
			matches = matchTargets(fmt.Sprint(query), pool)
		}
		if len(matches) != 1 {
			return nil, matches, true
		}
		windowID = text(matches[0], "window_id")
	}
	pool := []object{}
	multiple := len(sessions) > 1
	for _, v := range sessions {
		if wants("session") {
			pool = append(pool, sessionTarget(v))
		}
		for j, w := range v.windows {
			if windowID != "" && windowID != w.id {
				continue
			}
			if wants("window") {
				pool = append(pool, windowTarget(v, w, j+1))
			}
			if !wants("block") {
				continue
			}
			for _, bid := range w.blockIDs() {
				if b := v.blocks[bid]; b != nil {
					where := fmt.Sprintf("window %d", j+1)
					if multiple {
						where += " of " + v.label
					}
					pool = append(pool, object{"kind": "block", "session_id": v.id, "window_id": w.id, "block_id": b.id, "label": b.label, "where": where})
				}
			}
		}
		if wants("block") && windowID == "" {
			for _, bid := range v.blockOrder {
				w, _, _ := v.placement(bid)
				if b := v.blocks[bid]; w == nil && b != nil {
					where := "detached"
					if multiple {
						where += " in " + v.label
					}
					pool = append(pool, object{"kind": "block", "session_id": v.id, "block_id": b.id, "label": b.label, "where": where})
				}
			}
		}
	}
	return pool, nil, false
}

func defaultSession(pool []object, sid string) []object {
	for _, item := range pool {
		if item["session_id"] == sid {
			return []object{item}
		}
	}
	return pool
}

func (s *Server) defaultWindows(sessions []*session, context object, windowID string) []object {
	result := []object{}
	for _, v := range sessions {
		w := v.findWindow(windowID)
		if windowID == "" {
			if candidate, _, _ := v.placement(text(context, "block_id")); candidate != nil {
				w = candidate
			}
		}
		for j, item := range v.windows {
			if item == w {
				result = append(result, windowTarget(v, w, j+1))
			}
		}
	}
	return result
}

func (s *Server) defaultTargets(a object, pool []object) []object {
	kinds, context := stringList(a["kinds"]), asObject(a["context"])
	for _, kind := range []string{"client", "block", "window", "session"} {
		if !slices.Contains(kinds, kind) {
			continue
		}
		for _, item := range pool {
			if item["kind"] == kind && text(item, kind+"_id") == text(context, kind+"_id") {
				return []object{item}
			}
		}
		if kind == "client" {
			result := []object{}
			for _, item := range pool {
				if c := s.findClient(text(item, "client_id")); c != nil && text(c.info, "kind") == "app" {
					result = append(result, item)
				}
			}
			return result
		}
		if kind == "session" {
			result := []object{}
			for _, item := range pool {
				if item["kind"] == kind {
					result = append(result, item)
				}
			}
			return result
		}
		sessions := []*session{}
		for _, v := range s.sessions {
			for _, item := range pool {
				if item["session_id"] == v.id {
					sessions = append(sessions, v)
					break
				}
			}
		}
		// A window scope has already narrowed the pool to one window.
		windowID := ""
		if _, scoped := asObject(a["scope"])["window"]; scoped && len(pool) > 0 {
			windowID = text(pool[0], "window_id")
		}
		windows := s.defaultWindows(sessions, context, windowID)
		result := []object{}
		for _, window := range windows {
			for _, item := range pool {
				if kind == "window" && item["window_id"] == window["window_id"] && item["kind"] == kind {
					result = append(result, item)
				}
				if kind == "block" {
					v, _ := s.findSession(text(window, "session_id"))
					w := v.findWindow(text(window, "window_id"))
					if item["block_id"] == w.focused {
						result = append(result, item)
					}
				}
			}
		}
		return result
	}
	return nil
}

func targetResult(query string, found []object) object {
	if len(found) == 1 {
		return object{"query": query, "status": "resolved", "target": found[0]}
	}
	if len(found) > 1 {
		return object{"query": query, "status": "ambiguous", "candidates": found}
	}
	return object{"query": query, "status": "not_found", "reason": "no matching target"}
}

func (s *Server) resolve(a object) object {
	pool, scopeMatches, failed := s.targetPool(a)
	results := []any{}
	for _, query := range stringList(a["queries"]) {
		var found []object
		if failed {
			found = scopeMatches
		} else if strings.TrimSpace(query) == "" {
			found = s.defaultTargets(a, pool)
		} else {
			found = matchTargets(query, pool)
		}
		results = append(results, targetResult(query, found))
	}
	return object{"results": results}
}

func validIDSuffix(value string) bool {
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	_, err := hex.DecodeString(strings.Repeat("0", len(value)%2) + value)
	return err == nil
}

func (s *Server) targetRequest(method string, a object) (object, error) {
	kinds := stringList(a["kinds"])
	seen := map[string]bool{}
	for _, kind := range kinds {
		if seen[kind] || !slices.Contains([]string{"session", "window", "block", "client"}, kind) {
			return nil, bad("kinds must contain unique session, window, block or client values")
		}
		seen[kind] = true
	}
	if len(kinds) == 0 || seen["client"] && len(kinds) != 1 {
		return nil, bad("kinds is required; client must be the only kind")
	}
	scope := asObject(a["scope"])
	if seen["session"] && a["scope"] != nil || (seen["window"] || seen["client"]) && text(scope, "window") != "" {
		return nil, bad("scope is not valid for the requested target kinds")
	}
	if method == "target.resolve" {
		if len(stringList(a["queries"])) == 0 {
			return nil, bad("queries needs at least one query")
		}
		return s.resolve(a), nil
	}
	pool, _, _ := s.targetPool(a)
	prefix := strings.ToLower(strings.TrimSpace(text(a, "prefix")))
	completions := []any{}
	for _, kind := range kinds {
		for _, form := range []string{"label", "ordinal", "id"} {
			group := []object{}
			for _, target := range pool {
				if text(target, "kind") != kind {
					continue
				}
				value := text(target, "label")
				switch form {
				case "ordinal":
					if kind != "window" {
						continue
					}
					value = fmt.Sprint(target["ordinal"])
				case "id":
					id := strings.ReplaceAll(text(target, kind+"_id"), "-", "")
					if len(id) < 6 {
						continue
					}
					value = id[len(id)-6:]
				}
				if value != "" && strings.HasPrefix(strings.ToLower(value), prefix) {
					group = append(group, object{"value": value, "form": form, "target": target})
				}
			}
			sort.SliceStable(group, func(i, j int) bool {
				if form == "ordinal" {
					x, _ := strconv.Atoi(text(group[i], "value"))
					y, _ := strconv.Atoi(text(group[j], "value"))
					return x < y
				}
				return strings.ToLower(text(group[i], "value")) < strings.ToLower(text(group[j], "value"))
			})
			for _, value := range group {
				completions = append(completions, value)
			}
		}
	}
	return object{"completions": completions}, nil
}
