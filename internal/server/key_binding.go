package server

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	ghostty "go.mitchellh.com/libghostty"
)

func canonicalBinding(value string) (string, error) {
	original := value
	mods := map[string]bool{}
	for {
		before, after, found := strings.Cut(value, "+")
		if !found || before == "" {
			break
		}
		name := ""
		switch strings.ToLower(strings.TrimSpace(before)) {
		case "ctrl", "control":
			name = "ctrl"
		case "alt":
			name = "alt"
		case "shift":
			name = "shift"
		case "cmd", "super":
			name = "cmd"
		default:
			return "", fmt.Errorf("invalid key binding %q", original)
		}
		mods[name] = true
		value = after
	}
	key := strings.TrimSpace(value)
	if value == " " {
		key = "space"
	}
	if key == "" || strings.ContainsFunc(key, unicode.IsSpace) {
		return "", fmt.Errorf("invalid key binding %q", original)
	}
	if strings.HasPrefix(key, "[") {
		if !strings.HasSuffix(key, "]") || len(key) < 3 {
			return "", fmt.Errorf("invalid key binding %q", original)
		}
		name, found := physicalBindingNames[strings.ToLower(key[1:len(key)-1])]
		if !found {
			return "", fmt.Errorf("invalid key binding %q", original)
		}
		key = "[" + name + "]"
	} else {
		key = strings.ToLower(key)
		switch key {
		case "escape":
			key = "esc"
		}
		if utf8.RuneCountInString(key) > 1 && key != "space" && key != "esc" {
			name := key
			if key == "left" || key == "right" || key == "up" || key == "down" {
				name = "arrow_" + key
			}
			if _, err := ghostty.ParseKey(name); err != nil {
				return "", fmt.Errorf("invalid key binding %q", original)
			}
		}
	}
	parts := []string{}
	for _, modifier := range []string{"ctrl", "alt", "shift", "cmd"} {
		if mods[modifier] {
			parts = append(parts, modifier)
		}
	}
	return strings.Join(append(parts, key), "+"), nil
}

var physicalBindingNames = func() map[string]string {
	result := map[string]string{}
	for key := ghostty.Key(1); key < 512; key++ {
		name := key.String()
		if name == "unidentified" {
			continue
		}
		parts := strings.Split(name, "_")
		for i, part := range parts {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
		physical := strings.Join(parts, "")
		result[strings.ToLower(physical)] = physical
	}
	return result
}()

func canonicalKeys(value any) ([]any, error) {
	keys, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("keys must be an array")
	}
	result := make([]any, len(keys))
	for i, value := range keys {
		key, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("key %d must be a string", i)
		}
		canonical, err := canonicalBinding(key)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		result[i] = canonical
	}
	return result, nil
}
