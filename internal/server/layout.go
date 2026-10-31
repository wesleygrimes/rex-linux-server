package server

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
)

func (v *session) addBlock(b *block) {
	v.notifyRemote("block_created", object{"block_id": b.id})
	v.blocks[b.id] = b
	v.blockOrder = append(v.blockOrder, b.id)
}
func (v *session) dropBlock(bid string) {
	delete(v.blocks, bid)
	v.notifyRemote("block_closed", object{"block_id": bid})
	v.blockOrder = slices.DeleteFunc(v.blockOrder, func(id string) bool { return id == bid })
}
func nodeBlocks(n *layoutNode) []string {
	if n == nil {
		return nil
	}
	if n.blockID != "" {
		return []string{n.blockID}
	}
	return append(nodeBlocks(n.before), nodeBlocks(n.after)...)
}
func (w *window) hasLayer(target *layer) bool { return slices.Contains(w.layers, target) }
func (v *session) placement(bid string) (*window, *layer, *layoutNode) {
	for _, w := range v.windows {
		for _, l := range w.layers {
			if n := findNode(l.root, bid); n != nil {
				return w, l, n
			}
		}
	}
	return nil, nil, nil
}
func (v *session) findLayer(lid string) (*window, *layer, int) {
	for _, w := range v.windows {
		for i, l := range w.layers {
			if l.id == lid {
				return w, l, i
			}
		}
	}
	return nil, nil, -1
}
func removalSplit(n *layoutNode, bid string) string {
	if n == nil || n.blockID != "" {
		return ""
	}
	if n.before.blockID == bid || n.after.blockID == bid {
		return n.splitID
	}
	if found := removalSplit(n.before, bid); found != "" {
		return found
	}
	return removalSplit(n.after, bid)
}
func (v *session) removePlacement(bid string) (*window, *layer, string) {
	w, l, _ := v.placement(bid)
	if l == nil {
		return nil, nil, ""
	}
	oldIDs := nodeBlocks(l.root)
	index := slices.Index(oldIDs, bid)
	removed := removalSplit(l.root, bid)
	l.root = removeNode(l.root, bid)
	if w.zoomed == bid {
		w.zoomed = ""
	}
	if w.focused == bid {
		remaining := nodeBlocks(l.root)
		w.focused = ""
		if len(remaining) > 0 {
			w.focused = remaining[min(index, len(remaining)-1)]
		}
	}
	layerIndex := slices.Index(w.layers, l)
	if l.root == nil && l.kind == "floating" {
		w.layers = slices.Delete(w.layers, layerIndex, layerIndex+1)
	}
	if w.focused == "" {
		for i := min(layerIndex, len(w.layers)-1); i < len(w.layers) && i >= 0; i++ {
			if bid := firstBlock(w.layers[i].root); bid != "" {
				w.focused = bid
				break
			}
		}
		for i := min(layerIndex-1, len(w.layers)-1); w.focused == "" && i >= 0; i-- {
			w.focused = firstBlock(w.layers[i].root)
		}
	}
	return w, l, removed
}
func (s *Server) blockDescription(v *session, b *block) object {
	out := object{"block_id": b.id, "label": b.label, "creator_name": b.creatorName(), "flavor": b.flavorName(), "host_id": s.hostID, "placement": "detached"}
	if w, _, _ := v.placement(b.id); w != nil {
		out["placement"], out["window_id"] = "placed", w.id
	}
	return out
}
func (s *Server) focusBlock(v *session, bid string) (any, error) {
	w, _, _ := v.placement(bid)
	if w == nil {
		return nil, missing("block not found")
	}
	if v.active == w.id && w.focused == bid {
		return object{"revision": v.revision}, nil
	}
	if w.focused != bid {
		w.zoomed = ""
	}
	w.focused, v.active = bid, w.id
	return v.changed(s), nil
}
func normalizedBounds(value any) (rect, error) {
	a := asObject(value)
	r := rect{}
	for key, dst := range map[string]*float64{"x": &r.X, "y": &r.Y, "w": &r.W, "h": &r.H} {
		n, ok := a[key].(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return r, bad("input: validating: bounds must be a finite normalized rectangle with positive size contained within the unit square")
		}
		*dst = n
	}
	if r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 || r.X+r.W > 1 || r.Y+r.H > 1 {
		return r, bad("input: validating: bounds must be a finite normalized rectangle with positive size contained within the unit square")
	}
	return r, nil
}
func (s *Server) layoutRequest(c *client, v *session, method string, a object) (any, error, bool) {
	switch method {
	case "session.new_block", "session.new_layer", "session.float_block", "session.detach_block", "session.close_layer", "session.raise_layer", "session.lower_layer", "session.resize_layer", "session.move_block", "session.swap_blocks", "session.resize_pane", "session.zoom_block", "session.move_window", "session.focus_direction", "session.focus_next_pane", "session.focus_previous_pane", "session.focus_next_window", "session.focus_previous_window", "session.list_clients", "session.help":
		result, err := s.mutateLayout(c, v, method, a)
		return result, err, true
	}
	return nil, nil, false
}
func (s *Server) mutateLayout(c *client, v *session, method string, a object) (any, error) {
	unchanged := func() (any, error) { return object{"revision": v.revision}, nil }
	switch method {
	case "session.help":
		return s.help(), nil
	case "session.list_clients":
		clients := []any{}
		active := []*client{}
		for _, cl := range s.clients {
			if cl.conn != nil && cl.attached[v.id] {
				active = append(active, cl)
			}
		}
		sort.SliceStable(active, func(i, j int) bool { return active[i].connectedAt.Before(active[j].connectedAt) })
		for _, cl := range active {
			clients = append(clients, object{"client_id": cl.id, "principal": cl.principal, "info": maps.Clone(cl.info), "connected_at": cl.connectedAt, "connection_state": "connected"})
		}
		return object{"clients": clients}, nil
	case "session.new_block":
		if !s.blockCapacity(v) {
			return nil, bad("server block limit reached")
		}
		if text(a, "flavor") != "com.superlogical.terminal.shell" && s.pluginFlavor(text(a, "flavor")) == nil {
			return nil, bad("unsupported block flavor")
		}
		b, err := s.newBlock(v.id, a)
		if err != nil {
			return nil, err
		}
		v.addBlock(b)
		return object{"block_id": b.id, "revision": v.revision}, nil
	case "session.new_layer":
		if a["layout"] == nil {
			return nil, bad("input: validating: layout is required")
		}
		bounds, err := normalizedBounds(a["bounds"])
		if err != nil {
			return nil, err
		}
		w := v.findWindow(text(a, "window_id"))
		if w == nil {
			return nil, missing("window not found")
		}
		root, ids, err := s.makeLayout(v, asObject(a["layout"]))
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
		l := &layer{id: id("layer"), kind: "floating", bounds: bounds, root: root}
		w.layers = append(w.layers, l)
		if focus, _ := a["focus"].(bool); focus {
			w.zoomed = ""
			w.focused, v.active = ids[0], w.id
		}
		v.changed(s)
		return object{"layer_id": l.id, "block_ids": ids, "revision": v.revision}, nil
	case "session.float_block":
		bounds, err := normalizedBounds(a["bounds"])
		if err != nil {
			return nil, err
		}
		bid := text(a, "block_id")
		w, l, _ := v.placement(bid)
		if w == nil {
			return nil, missing(fmt.Sprintf("block %s not found in any window", bid))
		}
		if l.kind == "floating" {
			return nil, &apiError{"internal", fmt.Sprintf("block %s is already in floating layer %s", bid, l.id), 500}
		}
		focused := w.focused == bid
		v.removePlacement(bid)
		l = &layer{id: id("layer"), kind: "floating", bounds: bounds, root: &layoutNode{blockID: bid}}
		w.layers = append(w.layers, l)
		if focused {
			w.focused = bid
		}
		v.changed(s)
		return object{"layer_id": l.id, "revision": v.revision}, nil
	case "session.detach_block":
		bid := text(a, "block_id")
		if v.blocks[bid] == nil {
			return nil, missing("block not found")
		}
		if w, _, _ := v.removePlacement(bid); w == nil {
			return unchanged()
		}
		return v.changed(s), nil
	case "session.close_layer":
		_, l, _ := v.findLayer(text(a, "layer_id"))
		if l == nil {
			return nil, missing("layer not found")
		}
		if l.kind != "floating" {
			return nil, &apiError{"internal", fmt.Sprintf("cannot close tiled layer %s", l.id), 500}
		}
		for _, bid := range nodeBlocks(l.root) {
			v.blocks[bid].close()
			v.removePlacement(bid)
			v.dropBlock(bid)
			s.sessionEvent(v.id, "block_closed", object{"block_id": bid})
		}
		return v.changed(s), nil
	case "session.resize_layer":
		bounds, err := normalizedBounds(a["bounds"])
		if err != nil {
			return nil, err
		}
		_, l, _ := v.findLayer(text(a, "layer_id"))
		if l == nil {
			return nil, missing("layer not found")
		}
		if l.kind != "floating" {
			return nil, &apiError{"internal", fmt.Sprintf("cannot resize tiled layer %s", l.id), 500}
		}
		if l.bounds == bounds {
			return unchanged()
		}
		l.bounds = bounds
		return v.changed(s), nil
	case "session.raise_layer", "session.lower_layer":
		w, l, index := v.findLayer(text(a, "layer_id"))
		if l == nil {
			return nil, missing("layer not found")
		}
		if l.kind != "floating" {
			return nil, &apiError{"internal", fmt.Sprintf("cannot reorder tiled layer %s", l.id), 500}
		}
		relative := text(a, "relative_layer_id")
		var anchor *layer
		if relative != "" {
			other, found, _ := v.findLayer(relative)
			if other != w || found == nil {
				return nil, missing("relative layer not found in this window")
			}
			anchor = found
			if anchor == l {
				return unchanged()
			}
		}
		layers := slices.Clone(w.layers)
		layers = slices.Delete(layers, index, index+1)
		to := 1
		if method == "session.raise_layer" {
			to = len(layers)
		}
		if anchor != nil {
			to = slices.Index(layers, anchor)
			if method == "session.raise_layer" {
				to++
			}
			to = max(1, to)
		}
		if to == index {
			return unchanged()
		}
		w.layers = slices.Insert(layers, to, l)
		return v.changed(s), nil
	case "session.move_block":
		return s.moveBlock(v, a)
	case "session.swap_blocks":
		bid, other := text(a, "block_id"), text(a, "other_block_id")
		w1, _, n1 := v.placement(bid)
		w2, _, n2 := v.placement(other)
		if n1 == nil || n2 == nil {
			return nil, missing("block not found in a window")
		}
		if n1 == n2 {
			return unchanged()
		}
		n1.blockID, n2.blockID = n2.blockID, n1.blockID
		windows := []*window{w1}
		if w2 != w1 {
			windows = append(windows, w2)
		}
		for _, w := range windows {
			switch w.focused {
			case bid:
				w.focused = other
			case other:
				w.focused = bid
			}
			switch w.zoomed {
			case bid:
				w.zoomed = other
			case other:
				w.zoomed = bid
			}
		}
		return v.changed(s), nil
	case "session.resize_pane":
		direction := text(a, "direction")
		if direction != "horizontal" && direction != "vertical" {
			return nil, bad("invalid split direction")
		}
		ratio, ok := a["ratio"].(float64)
		if !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 || ratio >= 1 {
			return nil, bad("ratio must be between zero and one")
		}
		for _, w := range v.windows {
			for _, l := range w.layers {
				if n := splitBoundary(l.root, text(a, "before_block_id"), text(a, "after_block_id"), direction); n != nil {
					if n.ratio == ratio {
						return unchanged()
					}
					n.ratio = ratio
					return v.changed(s), nil
				}
			}
		}
		return nil, missing("split boundary not found")
	case "session.zoom_block":
		bid := text(a, "block_id")
		w := v.findWindow(text(a, "window_id"))
		if bid != "" {
			var placed *window
			placed, _, _ = v.placement(bid)
			if placed == nil {
				return nil, missing("block not found in a window")
			}
			if text(a, "window_id") != "" && w != placed {
				return nil, bad("block is not in the requested window")
			}
			w = placed
		}
		if w == nil {
			return nil, missing("window not found")
		}
		if bid == "" {
			bid = w.focused
		}
		zoom, explicit := a["zoom"].(bool)
		if !explicit {
			zoom = w.zoomed != bid
		}
		if zoom {
			if _, err := s.focusBlock(v, bid); err != nil {
				return nil, err
			}
			if w.zoomed != bid {
				w.zoomed = bid
				v.changed(s)
			}
		} else if w.zoomed != "" {
			bid = w.zoomed
			w.zoomed = ""
			v.changed(s)
		}
		return object{"block_id": bid, "zoomed": zoom, "revision": v.revision}, nil
	case "session.move_window":
		w := v.findWindow(text(a, "window_id"))
		if w == nil {
			return nil, missing("window not found")
		}
		var before *window
		if target := text(a, "before_window_id"); target != "" {
			before = v.findWindow(target)
			if before == nil {
				return nil, missing("before window not found")
			}
			if before == w {
				return unchanged()
			}
		}
		old := slices.Index(v.windows, w)
		windows := slices.Delete(slices.Clone(v.windows), old, old+1)
		to := len(windows)
		if before != nil {
			to = slices.Index(windows, before)
		}
		if to == old {
			return unchanged()
		}
		v.windows = slices.Insert(windows, to, w)
		return v.changed(s), nil
	case "session.focus_next_window", "session.focus_previous_window":
		w := v.findWindow("")
		if w == nil {
			return nil, missing("window not found")
		}
		i := slices.Index(v.windows, w)
		next := i + 1
		if strings.Contains(method, "previous") {
			next = i - 1
		}
		if wrap, _ := a["wrap"].(bool); wrap {
			next = (next + len(v.windows)) % len(v.windows)
		}
		if next < 0 || next >= len(v.windows) || next == i {
			return unchanged()
		}
		v.active = v.windows[next].id
		return v.changed(s), nil
	case "session.focus_next_pane", "session.focus_previous_pane":
		w := v.findWindow("")
		if w == nil {
			return nil, missing("window not found")
		}
		ids := w.blockIDs()
		if len(ids) == 0 {
			return unchanged()
		}
		i := slices.Index(ids, w.focused)
		next := i + 1
		if strings.Contains(method, "previous") {
			next = i - 1
		}
		if wrap, _ := a["wrap"].(bool); wrap {
			next = (next + len(ids)) % len(ids)
		}
		if next < 0 || next >= len(ids) || next == i {
			return unchanged()
		}
		return s.focusBlock(v, ids[next])
	case "session.focus_direction":
		return s.focusDirection(v, text(a, "direction"))
	}
	return nil, missing("layout method not found")
}
func (w *window) blockIDs() []string {
	out := []string{}
	for _, l := range w.layers {
		out = append(out, nodeBlocks(l.root)...)
	}
	return out
}
func (s *Server) blockCapacity(v *session) bool {
	count := len(v.blocks)
	for _, other := range s.sessions {
		if other != v {
			count += len(other.blocks)
		}
	}
	return !exceeds(count+1, s.limits.Blocks)
}
func splitBoundary(n *layoutNode, before, after, direction string) *layoutNode {
	if n == nil || n.blockID != "" {
		return nil
	}
	if n.direction == direction && contains(n.before, before) && contains(n.after, after) {
		return n
	}
	if found := splitBoundary(n.before, before, after, direction); found != nil {
		return found
	}
	return splitBoundary(n.after, before, after, direction)
}
func (s *Server) moveBlock(v *session, a object) (any, error) {
	bid, anchor := text(a, "block_id"), text(a, "anchor_block_id")
	if v.blocks[bid] == nil {
		return nil, missing("block not found")
	}
	w, l, _ := v.placement(anchor)
	if l == nil {
		return nil, missing("anchor block not found")
	}
	if bid == anchor {
		return nil, bad("a block cannot be its own anchor")
	}
	direction := text(a, "direction")
	if direction == "" {
		direction = "horizontal"
	}
	if direction != "horizontal" && direction != "vertical" {
		return nil, bad("invalid split direction")
	}
	side := text(a, "side")
	if side == "" {
		side = "after"
	}
	if side != "before" && side != "after" {
		return nil, bad("invalid split side")
	}
	ratio := .5
	if supplied, ok := a["ratio"].(float64); ok && supplied != 0 {
		ratio = supplied
	}
	if ratio <= 0 || ratio >= 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return nil, bad("ratio must be between zero and one")
	}
	source, _, _ := v.placement(bid)
	if source != w && exceeds(len(w.blockIDs())+1, s.limits.Panes) {
		return nil, bad("window pane limit reached")
	}
	wasFocused := source == w && w.focused == bid
	_, _, removed := v.removePlacement(bid)
	n := findNode(l.root, anchor)
	old := *n
	*n = layoutNode{splitID: id("split"), direction: direction, ratio: ratio, before: &old, after: &layoutNode{blockID: bid}}
	if side == "before" {
		n.before, n.after = n.after, n.before
	}
	if wasFocused {
		w.focused = bid
	}
	v.changed(s)
	out := object{"created_split_id": n.splitID, "revision": v.revision}
	if removed != "" {
		out["removed_split_id"] = removed
	}
	return out, nil
}
func (s *Server) focusDirection(v *session, direction string) (any, error) {
	if direction != "left" && direction != "right" && direction != "up" && direction != "down" {
		return nil, bad("invalid focus direction")
	}
	w := v.findWindow("")
	if w == nil {
		return nil, missing("window not found")
	}
	positions := map[string]rect{}
	var walk func(*layoutNode, rect)
	walk = func(n *layoutNode, r rect) {
		if n == nil {
			return
		}
		if n.blockID != "" {
			positions[n.blockID] = r
			return
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
		walk(n.before, a)
		walk(n.after, b)
	}
	for _, l := range w.layers {
		walk(l.root, l.bounds)
	}
	from, ok := positions[w.focused]
	if !ok {
		return object{"revision": v.revision}, nil
	}
	ids := w.blockIDs()
	nearest := func(from rect) string {
		best, score := "", math.Inf(1)
		for _, bid := range ids {
			if bid == w.focused {
				continue
			}
			r := positions[bid]
			if direction == "left" && r.X+r.W > from.X || direction == "right" && r.X < from.X+from.W || direction == "up" && r.Y+r.H > from.Y || direction == "down" && r.Y < from.Y+from.H {
				continue
			}
			dx, dy := r.X-from.X, r.Y-from.Y
			distance := dx*dx + dy*dy
			if distance <= score {
				best, score = bid, distance
			}
		}
		return best
	}
	best := nearest(from)
	if best == "" {
		switch direction {
		case "left":
			from.X++
		case "right":
			from.X--
		case "up":
			from.Y++
		case "down":
			from.Y--
		}
		best = nearest(from)
	}
	if best == "" {
		return object{"revision": v.revision}, nil
	}
	return s.focusBlock(v, best)
}
