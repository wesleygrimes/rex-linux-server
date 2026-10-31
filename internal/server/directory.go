package server

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func (b *block) foregroundPID() int {
	if group, err := unix.IoctlGetInt(int(b.file.Fd()), unix.TIOCGPGRP); err == nil && group > 0 {
		return group
	}
	return b.cmd.Process.Pid
}

func (b *block) workingDir() string {
	if cwd, err := processCwd(b.foregroundPID()); err == nil {
		return cwd
	}
	if cwd, err := processCwd(b.cmd.Process.Pid); err == nil {
		return cwd
	}
	home, _ := os.UserHomeDir()
	return home
}

func (b *block) listDir(a object) (any, error) {
	home, _ := os.UserHomeDir()
	path := text(a, "path")
	switch {
	case path == "~" || strings.HasPrefix(path, "~/"):
		path = filepath.Clean(home + strings.TrimPrefix(path, "~"))
	case filepath.IsAbs(path):
		path = filepath.Clean(path)
	default:
		path = filepath.Join(b.workingDir(), path)
	}
	entries := []any{}
	result := object{"path": path, "home": home, "status": "ok", "entries": entries, "truncated": false}
	failed := func(err error) (any, error) {
		switch {
		case errors.Is(err, os.ErrNotExist):
			result["status"] = "missing"
		case errors.Is(err, syscall.ENOTDIR):
			result["status"] = "not_dir"
		case errors.Is(err, os.ErrPermission):
			result["status"] = "denied"
		default:
			return nil, err
		}
		return result, nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return failed(err)
	}
	defer dir.Close()
	limit := number(a, "limit", 2000)
	if limit <= 0 {
		limit = 2000
	}
	prefix := strings.ToLower(text(a, "prefix"))
	files, _ := a["files"].(bool)
	encodedPath, _ := json.Marshal(path)
	encodedHome, _ := json.Marshal(home)
	budget, used := 0xbc00-len(encodedPath)-len(encodedHome), 0
	for {
		// Directory results preserve filesystem order.
		items, err := dir.ReadDir(128)
		for _, entry := range items {
			if !strings.HasPrefix(strings.ToLower(entry.Name()), prefix) {
				continue
			}
			symlink := entry.Type()&os.ModeSymlink != 0
			isDir := entry.IsDir()
			if symlink {
				info, err := os.Stat(filepath.Join(path, entry.Name()))
				if err != nil {
					continue
				}
				isDir = info.IsDir()
			}
			if !isDir && !files {
				continue
			}
			if len(entries) >= limit {
				result["truncated"] = true
				return result, nil
			}
			item := object{"name": entry.Name()}
			if symlink {
				item["symlink"] = true
			}
			if !isDir {
				item["file"] = true
			}
			encoded, _ := json.Marshal(item)
			cost := len(encoded)
			if len(entries) > 0 {
				cost++
			}
			if used+cost > budget {
				result["truncated"] = true
				return result, nil
			}
			used += cost
			entries = append(entries, item)
			result["entries"] = entries
		}
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return failed(err)
		}
	}
}
