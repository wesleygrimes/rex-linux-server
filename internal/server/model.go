package server

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type session struct {
	id, label   string
	revision    uint64
	windows     []*window
	active      string
	lastFocused string
	blocks      map[string]*block
	blockOrder  []string
}
type window struct {
	id, label, focused, zoomed string
	layers                     []*layer
}
type layer struct {
	id, kind string
	bounds   rect
	root     *layoutNode
}
type layoutNode struct {
	blockID, splitID, direction string
	ratio                       float64
	before, after               *layoutNode
}
type rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

var fullRect = rect{W: 1, H: 1}

func (s *Server) findSession(sid string) (*session, error) {
	for _, v := range s.sessions {
		if v.id == sid {
			return v, nil
		}
	}
	return nil, missing("session not found")
}
func (v *session) listed() object { return object{"session_id": v.id, "label": v.label} }
func (s *Server) listed() []any {
	out := []any{}
	for _, v := range s.sessions {
		out = append(out, v.listed())
	}
	return out
}
func (v *session) findWindow(wid string) *window {
	if wid == "" {
		wid = v.active
	}
	for _, w := range v.windows {
		if w.id == wid {
			return w
		}
	}
	return nil
}
func (v *session) findBlock(bid string) (*block, error) {
	if bid == "" {
		if w := v.findWindow(""); w != nil {
			bid = w.focused
		}
	}
	b := v.blocks[bid]
	if b == nil {
		return nil, missing("block not found")
	}
	return b, nil
}
func (v *session) changed(s *Server) object {
	v.revision++
	focused := ""
	if window := v.findWindow(""); window != nil {
		focused = window.focused
	}
	if focused != v.lastFocused {
		for _, item := range []struct{ id, action string }{{v.lastFocused, "out"}, {focused, "in"}} {
			if b := v.blocks[item.id]; b != nil {
				if b.remote != nil {
					b.remote.creator.process.contextEvents.push(object{"block_id": b.remote.id, "event": object{"type": "focus", "payload": object{"action": item.action}}})
				} else {
					b.input(object{"type": "focus", "payload": object{"action": item.action}})
				}
			}
		}
		v.lastFocused = focused
	}
	if _, err := s.findSession(v.id); err == nil {
		v.notifyRemote("session_view_changed", object{})
		s.sessionEvent(v.id, "session_view_changed", object{"revision": v.revision})
	}
	return object{"revision": v.revision}
}

func (s *Server) view(v *session) object {
	windows := []any{}
	var focused any
	for _, w := range v.windows {
		layers := []any{}
		for _, l := range w.layers {
			blocks := []any{}
			var node func(*layoutNode, rect) any
			node = func(n *layoutNode, r rect) any {
				if n == nil {
					return nil
				}
				o := object{"rect": r}
				if n.blockID != "" {
					o["block_id"] = n.blockID
					if n.blockID == w.focused {
						o["focused"] = true
					}
					if b := v.blocks[n.blockID]; b != nil {
						blockRect := r
						if b.id == w.zoomed {
							blockRect = fullRect
						}
						blocks = append(blocks, object{"block_id": b.id, "label": b.label, "creator_name": b.creatorName(), "flavor": b.flavorName(), "host_id": s.hostID, "rect": blockRect})
					}
					return o
				}
				a, b := r, r
				if n.direction == "horizontal" {
					a.W *= n.ratio
					b.X += a.W
					b.W -= a.W
				} else {
					a.H *= n.ratio
					b.Y += a.H
					b.H -= a.H
				}
				o["split"] = object{"split_id": n.splitID, "direction": n.direction, "ratio": n.ratio, "before": node(n.before, a), "after": node(n.after, b)}
				return o
			}
			layout := node(l.root, l.bounds)
			layers = append(layers, object{"layer_id": l.id, "kind": l.kind, "bounds": l.bounds, "layout": layout, "blocks": blocks})
		}
		wv := object{"window_id": w.id, "label": w.label, "layers": layers}
		if w.id == v.active {
			wv["active"] = true
		}
		if w.focused != "" {
			wv["focused_block_id"] = w.focused
		}
		if w.zoomed != "" {
			wv["zoomed_block_id"] = w.zoomed
		}
		windows = append(windows, wv)
		if w.id == v.active {
			focused = wv
		}
	}
	return object{"revision": v.revision, "session_id": v.id, "label": v.label, "active_window_id": v.active, "focused_window": focused, "windows": windows}
}

func (s *Server) makeLayout(v *session, spec object) (*layoutNode, []string, error) {
	if split, ok := spec["split"].(map[string]any); ok {
		direction := text(split, "direction")
		if direction != "horizontal" && direction != "vertical" {
			return nil, nil, bad("invalid split direction")
		}
		a, aa, err := s.makeLayout(v, asObject(split["before"]))
		if err != nil {
			return nil, nil, err
		}
		b, bb, err := s.makeLayout(v, asObject(split["after"]))
		if err != nil {
			for _, bid := range aa {
				v.blocks[bid].close()
				v.dropBlock(bid)
			}
			return nil, nil, err
		}
		ratio := 0.5
		if x, ok := split["ratio"].(float64); ok {
			ratio = x
		}
		if ratio <= 0 || ratio >= 1 {
			ratio = 0.5
		}
		return &layoutNode{splitID: id("split"), direction: direction, ratio: ratio, before: a, after: b}, append(aa, bb...), nil
	}
	if !s.blockCapacity(v) {
		return nil, nil, bad("server block limit reached")
	}
	input := asObject(spec["block"])
	flavor := text(input, "flavor")
	if flavor != "com.superlogical.terminal.shell" && s.pluginFlavor(flavor) == nil {
		return nil, nil, bad("unsupported block flavor")
	}
	b, err := s.newBlock(v.id, input)
	if err != nil {
		return nil, nil, err
	}
	v.addBlock(b)
	return &layoutNode{blockID: b.id}, []string{b.id}, nil
}
func (s *Server) newWindow(v *session, args object) (object, error) {
	if args["layout"] == nil {
		return nil, bad("layout is required")
	}
	if exceeds(len(v.windows)+1, s.limits.Windows) {
		return nil, bad("window limit reached")
	}
	root, ids, err := s.makeLayout(v, asObject(args["layout"]))
	if err != nil {
		return nil, err
	}
	if exceeds(len(ids), s.limits.Panes) {
		for _, bid := range ids {
			v.blocks[bid].close()
			v.dropBlock(bid)
		}
		return nil, bad("window pane limit reached")
	}
	w := &window{id: id("window"), label: text(args, "window_label"), focused: ids[0], layers: []*layer{{id: id("layer"), kind: "tiled", bounds: fullRect, root: root}}}
	if w.label == "" {
		w.label = "Shell"
	}
	v.windows = append(v.windows, w)
	if focus, ok := args["focus"].(bool); !ok || focus || v.active == "" {
		v.active = w.id
	}
	v.changed(s)
	return object{"window_id": w.id, "block_ids": ids, "revision": v.revision}, nil
}

func (s *Server) dispatch(c *client, method string, a object) (any, error) {
	switch method {
	case "help.all":
		return s.help(), nil
	case "target.resolve", "target.complete":
		return s.targetRequest(method, a)
	case "server.version":
		return s.version(), nil
	case "server.status":
		return s.status(), nil
	case "server.whoami":
		out := object{"principal": c.principal}
		if s.account != "" {
			out["account"] = s.account
		}
		return out, nil
	case "session.list":
		return object{"sessions": s.listed()}, nil
	case "session.attach_all":
		c.subscribed = true
		c.all = text(a, "event_scope") == "all"
		return object{"sessions": s.listed()}, nil
	case "session.bootstrap":
		c.subscribed = true
		c.all = text(a, "event_scope") == "all"
		out := object{"client_id": c.id, "server_status": s.status(), "attach_all": object{"sessions": s.listed()}, "selected_session": nil}
		if ids, ok := a["attach_session_ids"].([]any); ok {
			attached := []any{}
			seen := map[string]bool{}
			for _, sid := range ids {
				if v, err := s.findSession(fmt.Sprint(sid)); err == nil && !seen[v.id] {
					seen[v.id] = true
					s.attachClient(c, v)
					attached = append(attached, object{"session": v.listed(), "view": s.view(v)})
				}
			}
			if len(attached) > 0 {
				out["attached_sessions"] = attached
			}
			return out, nil
		}
		var selected *session
		if preferred := text(a, "preferred_session_id"); preferred != "" {
			selected, _ = s.findSession(preferred)
		}
		if selected == nil && len(s.sessions) > 0 {
			selected = s.sessions[0]
		}
		if selected != nil {
			s.attachClient(c, selected)
			view := s.view(selected)
			out["selected_session"] = object{"session": selected.listed(), "attachment": object{"client_id": c.id, "view": view}, "view": view}
		}
		return out, nil
	case "session.create":
		if exceeds(len(s.sessions)+1, s.limits.Sessions) {
			return nil, bad("session limit reached")
		}
		v := &session{id: id("session"), label: text(a, "label"), blocks: map[string]*block{}}
		if v.label == "" {
			v.label = "Shell"
		}
		initial := []any{}
		if windows, ok := a["initial_windows"].([]any); ok {
			for _, spec := range windows {
				w, err := s.newWindow(v, asObject(spec))
				if err != nil {
					for _, b := range v.blocks {
						b.close()
					}
					return nil, err
				}
				initial = append(initial, w)
			}
		}
		s.sessions = append(s.sessions, v)
		s.emitLocked("session_created", object{"session": v.listed()}, v.id)
		return object{"session_id": v.id, "initial_windows": initial}, nil
	case "client.key":
		v, err := s.findSession(text(a, "session_id"))
		if err != nil {
			return nil, err
		}
		if b, err := v.findBlock(text(a, "block_id")); err == nil {
			if _, err := b.input(object{"type": "key", "payload": a["key"]}); err != nil {
				return nil, err
			}
		}
		return object{"handled": false}, nil
	case "client.keymap":
		return s.keymapRequest(c, a)
	case "client.inspect":
		return s.inspectClient(a)
	case "client.reply":
		return s.replyClient(c, a)
	case "client.list":
		return s.listClients(a)
	case "session.detach":
		sid := text(a, "session_id")
		if c.attached[sid] {
			delete(c.attached, sid)
			if v, err := s.findSession(sid); err == nil {
				v.notifyRemote("client_disconnected", object{"client_id": c.id})
			}
			s.releaseClientSizes(c, sid)
			s.clientEventLocked("client_changed", object{"client": s.clientInfo(c)})
		}
		return object{}, nil
	case "hosts.list":
		return s.hostRequest(method, a)
	case "hosts.put", "hosts.remove":
		return s.hostRequest(method, a)
	case "block_creator.list":
		creators := []any{object{"name": "com.superlogical.terminal", "title": "Terminal", "description": "Persistent terminal", "host_id": s.hostID, "flavors": []any{object{"name": "shell", "flavor": "com.superlogical.terminal.shell", "title": "Shell", "description": "Persistent shell session", "options_schema": shellOptionsSchema()}}, "api": terminalAPI()}}
		names := []string{}
		for name := range s.creators {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			creators = append(creators, s.creators[name].info)
		}
		return object{"creators": creators}, nil

	}
	if method == "client.set_label" {
		if _, present := a["client_id"]; present {
			return nil, bad("client.set_label does not accept client_id")
		}
		label, ok := a["label"].(string)
		if !ok {
			return nil, bad("label must be a string")
		}
		label, err := clientLabel(label)
		if err != nil {
			return nil, err
		}
		if label == "" {
			delete(c.info, "label")
		} else {
			c.info["label"] = label
		}
		s.clientEventLocked("client_changed", object{"client": s.clientInfo(c)})
		return object{}, nil
	}
	if method == "client.set_script" {
		return object{}, nil
	}
	if strings.HasPrefix(method, "server.tailscale.") {
		return s.tailnetRequest(c, method, a)
	}
	v, err := s.findSession(text(a, "session_id"))
	if err != nil {
		return nil, err
	}
	if method != "session.attach" && !c.attached[v.id] {
		return nil, &apiError{"conflict", "stream is not attached to session " + v.id, 409}
	}
	switch method {
	case "session.list_blocks":
		blocks := []any{}
		for _, bid := range v.blockOrder {
			if b := v.blocks[bid]; b != nil {
				blocks = append(blocks, s.blockDescription(v, b))
			}
		}
		return object{"blocks": blocks}, nil
	case "session.attach":
		s.attachClient(c, v)
		return object{"client_id": c.id, "view": s.view(v)}, nil
	case "session.view":
		return s.view(v), nil
	case "session.set_label":
		v.label = text(a, "label")
		return v.changed(s), nil
	case "session.destroy":
		for _, attached := range s.clients {
			if attached.attached[v.id] {
				delete(attached.attached, v.id)
				v.notifyRemote("client_disconnected", object{"client_id": attached.id})
				s.clientEventLocked("client_changed", object{"client": s.clientInfo(attached)})
			}
		}
		for i, item := range s.sessions {
			if item == v {
				s.sessions = append(s.sessions[:i], s.sessions[i+1:]...)
				break
			}
		}
		for _, b := range v.blocks {
			b.close()
		}
		s.emitLocked("session_destroyed", object{"session_id": v.id}, v.id)
		return object{}, nil
	case "session.new_window":
		return s.newWindow(v, a)
	case "session.focus_window":
		w := v.findWindow(text(a, "window_id"))
		if w == nil {
			return nil, missing("window not found")
		}
		if v.active == w.id {
			return object{"revision": v.revision}, nil
		}
		v.active = w.id
		return v.changed(s), nil
	case "session.set_window_label":
		w := v.findWindow(text(a, "window_id"))
		if w == nil {
			return nil, missing("window not found")
		}
		w.label = text(a, "label")
		return v.changed(s), nil
	case "session.focus_block":
		bid := text(a, "block_id")
		return s.focusBlock(v, bid)
	case "session.new_split":
		anchor := text(a, "anchor_block_id")
		w := v.findWindow(text(a, "window_id"))
		if w == nil {
			return nil, missing("window not found")
		}
		if anchor == "" {
			anchor = w.focused
		}
		_, l, n := v.placement(anchor)
		if l == nil || !w.hasLayer(l) {
			return nil, missing("anchor block not found")
		}
		if n == nil {
			return nil, missing("anchor block not found")
		}
		dir := text(a, "direction")
		if dir != "horizontal" && dir != "vertical" {
			return nil, bad("invalid split direction")
		}
		fresh, ids, err := s.makeLayout(v, asObject(a["layout"]))
		if err != nil {
			return nil, err
		}
		if exceeds(len(w.blockIDs())+len(ids), s.limits.Panes) {
			for _, bid := range ids {
				v.blocks[bid].close()
				v.dropBlock(bid)
			}
			return nil, bad("window pane limit reached")
		}
		old := *n
		ratio := 0.5
		if r, ok := a["ratio"].(float64); ok && r > 0 && r < 1 {
			ratio = r
		}
		*n = layoutNode{splitID: id("split"), direction: dir, ratio: ratio, before: &old, after: fresh}
		if text(a, "side") == "before" {
			n.before, n.after = n.after, n.before
		}
		if focus, _ := a["focus"].(bool); focus {
			w.zoomed = ""
			w.focused = ids[0]
		}
		v.changed(s)
		return object{"anchor_block_id": anchor, "block_ids": ids, "split_ids": []string{n.splitID}, "revision": v.revision}, nil
	case "session.close_window":
		w := v.findWindow(text(a, "window_id"))
		if w == nil {
			return nil, missing("window not found")
		}
		for _, l := range w.layers {
			for _, bid := range nodeBlocks(l.root) {
				v.blocks[bid].close()
				v.dropBlock(bid)
				s.sessionEvent(v.id, "block_closed", object{"block_id": bid})
			}
		}
		for i, item := range v.windows {
			if item == w {
				v.windows = append(v.windows[:i], v.windows[i+1:]...)
				break
			}
		}
		if v.active == w.id {
			v.active = ""
			if len(v.windows) > 0 {
				v.active = v.windows[0].id
			}
		}
		return v.changed(s), nil
	case "data.connect":
		return s.issueTicket(c, v, a)
	}
	if result, err, handled := s.layoutRequest(c, v, method, a); handled {
		return result, err
	}
	b, err := v.findBlock(text(a, "block_id"))
	if err != nil {
		return nil, err
	}
	switch method {
	case "session.set_block_label":
		b.label = text(a, "label")
		return v.changed(s), nil
	case "session.describe_block":
		return object{"block": s.blockDescription(v, b), "api": b.api()}, nil
	case "block.close":
		b.close()
		v.removePlacement(b.id)
		v.dropBlock(b.id)
		s.sessionEvent(v.id, "block_closed", object{"block_id": b.id})
		return v.changed(s), nil
	case "block.method":
		args, provided := a["args"]
		if !provided {
			args = object{}
		}
		return b.method(c.id, text(a, "method"), args)
	case "block.event":
		return b.input(asObject(a["event"]))
	}
	if strings.HasPrefix(method, b.creatorName()+".") {
		args, provided := a["args"]
		if !provided {
			args = object{}
		}
		return b.method(c.id, strings.TrimPrefix(method, b.creatorName()+"."), args)
	}
	return nil, &apiError{"method_not_found", "method not implemented: " + method, 404}
}

func findNode(n *layoutNode, bid string) *layoutNode {
	if n == nil {
		return nil
	}
	if n.blockID == bid {
		return n
	}
	if a := findNode(n.before, bid); a != nil {
		return a
	}
	return findNode(n.after, bid)
}
func contains(n *layoutNode, bid string) bool { return findNode(n, bid) != nil }
func firstBlock(n *layoutNode) string {
	if n == nil {
		return ""
	}
	if n.blockID != "" {
		return n.blockID
	}
	return firstBlock(n.before)
}
func removeNode(n *layoutNode, bid string) *layoutNode {
	if n == nil || n.blockID == bid {
		return nil
	}
	if n.blockID != "" {
		return n
	}
	n.before = removeNode(n.before, bid)
	n.after = removeNode(n.after, bid)
	if n.before == nil {
		return n.after
	}
	if n.after == nil {
		return n.before
	}
	return n
}

func (s *Server) keymap(a object) object {
	bindings := []any{}
	modes := map[string]object{}
	diagnostics := []any{}
	for _, key := range []string{"defaults", "overrides"} {
		groups, _ := a[key].([]any)
		for _, group := range groups {
			g := asObject(group)
			declarations, _ := g["modes"].([]any)
			for _, value := range declarations {
				mode := asObject(value)
				modes[text(mode, "name")] = cloneObject(mode)
			}
			removals, _ := g["removals"].([]any)
			for _, item := range removals {
				r := asObject(item)
				keys, _ := r["keys"].([]any)
				mode := text(r, "mode")
				originalCount := len(bindings)
				kept := bindings[:0]
				for _, binding := range bindings {
					b := asObject(binding)
					bk, _ := b["keys"].([]any)
					matches := mode == text(b, "mode") && (len(keys) == 0 && mode != "" || slices.Equal(keys, bk)) && (text(r, "action") == "" || r["action"] == b["action"])
					if !matches {
						kept = append(kept, binding)
					}
				}
				bindings = kept
				if originalCount == len(kept) && key == "overrides" && !(mode != "" && text(r, "action") == "") {
					warning := object{"severity": "warning", "source": g["name"], "key": strings.Join(stringList(r["keys"]), " "), "message": "removal did not match a binding"}
					if mode != "" {
						warning["mode"] = mode
					}
					if text(r, "action") != "" {
						warning["action"] = r["action"]
					}
					diagnostics = append(diagnostics, warning)
				}
				if mode != "" && len(keys) == 0 {
					delete(modes, mode)
				} else if mode != "" && text(r, "action") == "" {
					if modes[mode] == nil {
						modes[mode] = object{"name": mode, "exclusive": false}
					}
					blocked, _ := modes[mode]["blocked"].([]string)
					key := strings.Join(stringList(r["keys"]), " ")
					if !slices.Contains(blocked, key) {
						blocked = append(blocked, key)
						slices.Sort(blocked)
					}
					modes[mode]["blocked"] = blocked
				}
			}
			items, _ := g["bindings"].([]any)
			for _, item := range items {
				b := asObject(item)
				binding := object{"keys": b["keys"], "action": b["action"], "source": g["name"]}
				for _, optional := range []string{"args", "mode"} {
					if value, ok := b[optional]; ok {
						binding[optional] = value
					}
				}
				bindings = append(bindings, binding)
				if mode := text(b, "mode"); mode != "" {
					if modes[mode] == nil {
						modes[mode] = object{"name": mode, "exclusive": false}
					}
				}
			}
		}
	}
	slices.Reverse(bindings)
	actions := []any{}
	if scope := text(a, "server_actions"); scope == "" || scope == "all" {
		blockActions := s.blockActions()
		names := []string{}
		for name := range blockActions {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			actions = append(actions, blockActions[name].record)
		}

	}
	modeList := []any{}
	names := make([]string, 0, len(modes))
	for name := range modes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		modeList = append(modeList, modes[name])
	}
	out := object{"bindings": bindings, "diagnostics": diagnostics, "config": s.configInfo()}
	if len(actions) > 0 {
		out["actions"] = actions
	}
	if len(modeList) > 0 {
		out["modes"] = modeList
	}
	return out
}
