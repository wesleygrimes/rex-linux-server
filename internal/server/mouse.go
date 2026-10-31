package server

import (
	"math"
	"slices"
	"strings"
	"time"

	ghostty "go.mitchellh.com/libghostty"
)

// Caller holds b.mu. Keep one encoder so motion deduplication survives events.
func (b *block) inputMouse(a object) (any, error) {
	button, err := ghostty.ParseMouseButton(text(a, "button"))
	if text(a, "button") == "" {
		button, err = ghostty.MouseButtonUnknown, nil
	}
	if err != nil {
		return nil, bad(err.Error())
	}
	mods, err := ghostty.ParseMods(text(a, "mods"))
	if err != nil {
		return nil, bad(err.Error())
	}
	tracking, err := b.term.MouseTracking()
	if err != nil {
		return nil, err
	}
	if !tracking || slices.Contains(strings.Split(text(a, "mods"), "+"), "shift") {
		return b.selectMouse(a, button)
	}
	var action ghostty.MouseAction
	switch text(a, "action") {
	case "", "press":
		action = ghostty.MouseActionPress
	case "release":
		action = ghostty.MouseActionRelease
	case "motion":
		action = ghostty.MouseActionMotion
	default:
		return nil, bad("unknown mouse action")
	}
	if b.mouse == nil {
		b.mouse, err = ghostty.NewMouseEncoder()
		if err != nil {
			return nil, err
		}
	}
	b.mouse.SetOptFromTerminal(b.term)
	b.mouse.SetOptSize(ghostty.MouseEncoderSize{ScreenWidth: uint32(b.cols * b.cellWidth), ScreenHeight: uint32(b.rows * b.cellHeight), CellWidth: uint32(b.cellWidth), CellHeight: uint32(b.cellHeight)})
	event, err := ghostty.NewMouseEvent()
	if err != nil {
		return nil, err
	}
	defer event.Close()
	event.SetButton(button)
	event.SetMods(mods)
	event.SetAction(action)
	x := (float32(number(a, "x", 0)) + 0.5) * float32(b.cellWidth)
	y := (float32(number(a, "y", 0)) + 0.5) * float32(b.cellHeight)
	if pixel, ok := a["pixel"].(map[string]any); ok {
		x, y = float32(number(pixel, "x", 0)), float32(number(pixel, "y", 0))
	}
	event.SetPosition(ghostty.MousePosition{X: x, Y: y})
	data, err := b.mouse.Encode(event)
	if err != nil {
		return nil, err
	}
	_, err = b.writer.Write(data)
	return object{}, err
}

func (b *block) inputWheel(a object) (any, error) {
	dx, _ := a["delta_x"].(float64)
	dy, _ := a["delta_y"].(float64)
	if dx == 0 && dy == 0 {
		return object{}, nil
	}
	tracking, err := b.term.MouseTracking()
	if err != nil {
		return nil, err
	}
	if !tracking {
		switch text(a, "delta_mode") {
		case "", "pixel":
			if b.cellHeight == 0 {
				return object{}, nil
			}
			dy /= float64(b.cellHeight)
		case "page":
			dy *= float64(b.rows)
		case "line":
		default:
			return nil, bad("unknown wheel delta mode")
		}
		b.term.ScrollViewportDelta(int(dy))
		return object{}, nil
	}
	button := "four"
	if math.Abs(dx) > math.Abs(dy) {
		button = "six"
		if dx > 0 {
			button = "seven"
		}
	} else if dy > 0 {
		button = "five"
	}
	return b.inputMouse(object{"button": button, "action": "press", "mods": a["mods"], "x": a["x"], "y": a["y"], "pixel": a["pixel"]})
}

func (b *block) selectMouse(a object, button ghostty.MouseButton) (any, error) {
	action := text(a, "action")
	if button != ghostty.MouseButtonLeft && !(action == "motion" && b.selecting) {
		return object{}, nil
	}
	kind := ghostty.SelectionGestureEventTypePress
	switch action {
	case "", "press":
		b.selecting = true
	case "motion":
		if !b.selecting {
			return object{}, nil
		}
		kind = ghostty.SelectionGestureEventTypeDrag
	case "release":
		kind = ghostty.SelectionGestureEventTypeRelease
	default:
		return nil, bad("unknown mouse action")
	}
	if b.selectionGesture == nil {
		var err error
		b.selectionGesture, err = ghostty.NewSelectionGesture()
		if err != nil {
			return nil, err
		}
	}
	event, err := ghostty.NewSelectionGestureEvent(kind)
	if err != nil {
		return nil, err
	}
	defer event.Close()
	x, y := max(0, min(b.cols-1, number(a, "x", 0))), max(0, min(b.rows-1, number(a, "y", 0)))
	ref, err := b.term.GridRef(ghostty.Point{Tag: ghostty.PointTagViewport, X: uint16(x), Y: uint32(y)})
	if err != nil {
		return nil, err
	}
	if err := event.SetRef(ref); err != nil {
		return nil, err
	}
	position := ghostty.SurfacePosition{X: (float64(x) + 0.5) * float64(b.cellWidth), Y: (float64(y) + 0.5) * float64(b.cellHeight)}
	if kind == ghostty.SelectionGestureEventTypePress {
		b.selectionX, b.selectionY = x, y
		b.selectionMoved = false
	}
	if kind == ghostty.SelectionGestureEventTypeDrag {
		b.selectionMoved = b.selectionMoved || x != b.selectionX || y != b.selectionY
		position.X = float64(x*b.cellWidth) + 0.1
		if y > b.selectionY || y == b.selectionY && x >= b.selectionX {
			position.X = float64((x+1)*b.cellWidth) - 0.1
		}
	}
	if pixel, ok := a["pixel"].(map[string]any); ok {
		position.X = float64(number(pixel, "x", 0))
		position.Y = float64(number(pixel, "y", 0))
	}
	if kind != ghostty.SelectionGestureEventTypeRelease {
		if err := event.SetPosition(position); err != nil {
			return nil, err
		}
	}
	if kind == ghostty.SelectionGestureEventTypePress {
		if err := event.SetTimeNS(uint64(time.Now().UnixNano())); err != nil {
			return nil, err
		}
		if err := event.SetRepeatIntervalNS(uint64(500 * time.Millisecond)); err != nil {
			return nil, err
		}
		if err := event.SetRepeatDistance(4); err != nil {
			return nil, err
		}
		if err := event.SetBehaviors(ghostty.SelectionGestureBehaviors{SingleClick: ghostty.SelectionGestureBehaviorCell, DoubleClick: ghostty.SelectionGestureBehaviorWord, TripleClick: ghostty.SelectionGestureBehaviorLine}); err != nil {
			return nil, err
		}
	}
	if kind == ghostty.SelectionGestureEventTypeDrag {
		if err := event.SetGeometry(ghostty.SelectionGestureGeometry{Columns: uint32(b.cols), CellWidth: uint32(max(1, b.cellWidth)), ScreenHeight: uint32(b.rows * max(1, b.cellHeight))}); err != nil {
			return nil, err
		}
		if err := event.SetRectangle(slices.Contains(strings.Split(text(a, "mods"), "+"), "alt")); err != nil {
			return nil, err
		}
	}
	selection, err := b.selectionGesture.Event(b.term, event)
	if err != nil {
		return nil, err
	}
	if selection != nil || kind == ghostty.SelectionGestureEventTypePress {
		if err := b.term.SetSelection(selection); err != nil {
			return nil, err
		}
	}
	if action == "release" {
		b.selecting = false
		copied, err := b.term.SelectionFormatString(ghostty.WithSelectionTrim(true), ghostty.WithSelectionUnwrap(true))
		if err != nil {
			return nil, err
		}
		if copied != "" {
			b.event("selection_copied", object{"text": copied})
		} else if !b.selectionMoved {
			b.term.SetSelection(nil)
			uri, err := ref.HyperlinkURI()
			if err != nil {
				return nil, err
			}
			if uri != "" {
				b.event("url_clicked", object{"url": uri})
			}
		}
	}
	return object{}, nil
}
