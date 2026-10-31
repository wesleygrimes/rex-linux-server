package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	ghostty "go.mitchellh.com/libghostty"
)

type terminalTheme struct {
	Foreground string   `json:"foreground,omitempty"`
	Background string   `json:"background,omitempty"`
	Cursor     string   `json:"cursor,omitempty"`
	Palette    []string `json:"palette,omitempty"`
	Scheme     string   `json:"scheme,omitempty"`
}

func themeColor(name, value string) (*ghostty.ColorRGB, error) {
	if value == "" {
		return nil, nil
	}
	rgb, err := hex.DecodeString(strings.TrimPrefix(value, "#"))
	if err != nil || len(rgb) != 3 {
		return nil, bad(fmt.Sprintf("input: validating: %s color %q must be #RRGGBB or RRGGBB", name, value))
	}
	return &ghostty.ColorRGB{R: rgb[0], G: rgb[1], B: rgb[2]}, nil
}

// Replacing a theme resets omitted defaults, as opposed to applying a patch.
// Validate every value before touching the emulator or the output stream.
func (b *block) setTheme(a object) (any, error) {
	theme := &terminalTheme{Foreground: text(a, "foreground"), Background: text(a, "background"), Cursor: text(a, "cursor"), Scheme: text(a, "scheme")}
	foreground, err := themeColor("foreground", theme.Foreground)
	if err != nil {
		return nil, err
	}
	background, err := themeColor("background", theme.Background)
	if err != nil {
		return nil, err
	}
	cursor, err := themeColor("cursor", theme.Cursor)
	if err != nil {
		return nil, err
	}
	palette := ghostty.DefaultPalette()
	if entries, ok := a["palette"].([]any); ok {
		for _, entry := range entries {
			value, ok := entry.(string)
			if !ok {
				return nil, bad("palette entries must be strings")
			}
			indexText, colorText, found := strings.Cut(value, "=")
			index, err := strconv.Atoi(indexText)
			if !found || err != nil || index < 0 || index >= len(palette) || colorText == "" {
				return nil, bad("palette entries must be INDEX=RRGGBB with an index from 0 to 255")
			}
			color, err := themeColor("palette", colorText)
			if err != nil {
				return nil, err
			}
			palette[index] = *color
			theme.Palette = append(theme.Palette, value)
		}
	} else if a["palette"] != nil {
		return nil, bad("palette must be an array")
	}
	scheme := ghostty.ColorSchemeDark
	switch theme.Scheme {
	case "":
		if background != nil && 0.299*float64(background.R)+0.587*float64(background.G)+0.114*float64(background.B) >= 128 {
			scheme = ghostty.ColorSchemeLight
		}
	case "light":
		scheme = ghostty.ColorSchemeLight
	case "dark":
	default:
		return nil, bad("scheme must be light or dark")
	}
	for _, item := range []struct {
		set   func(*ghostty.ColorRGB) error
		color *ghostty.ColorRGB
	}{{b.term.SetColorForeground, foreground}, {b.term.SetColorBackground, background}, {b.term.SetColorCursor, cursor}} {
		if err := item.set(item.color); err != nil {
			return nil, err
		}
	}
	if err := b.term.SetColorPalette(&palette); err != nil {
		return nil, err
	}
	changed := b.colorScheme != scheme
	b.theme, b.colorScheme = theme, scheme
	if report, _ := b.term.Mode(ghostty.ModeColorSchemeReport); report && changed {
		data, err := ghostty.ColorSchemeReportEncode(scheme)
		if err != nil {
			return nil, err
		}
		if _, err := b.writer.Write(data); err != nil {
			return nil, err
		}
	}
	err = b.scheduleMutation("theme", func() error {
		for d := range b.channels {
			d.append(b.themeMarker(d.nonce))
		}
		return nil
	})
	return object{}, err
}

func (b *block) themeMarker(nonce uint64) []byte {
	data, _ := json.Marshal(struct {
		Type string `json:"type"`
		*terminalTheme
	}{"theme", b.theme})
	return []byte(fmt.Sprintf("\x1b_rex1;%d;%s\x1b\\", nonce, data))
}
