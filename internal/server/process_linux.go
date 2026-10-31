package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func processCwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}

func processName(pid int) string {
	name, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	return strings.TrimSuffix(string(name), "\n")
}

func processDetails(pid int) (object, int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, 0, err
	}
	_, fields, ok := strings.Cut(string(data), ") ")
	parts := strings.Fields(fields)
	if !ok || len(parts) < 2 {
		return nil, 0, fmt.Errorf("invalid process stat for pid %d", pid)
	}
	parent, _ := strconv.Atoi(parts[1])
	info, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return nil, 0, err
	}
	uid := int(info.Sys().(*syscall.Stat_t).Uid)
	result := object{"pid": pid, "user_id": uid, "name": processName(pid)}
	if path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		result["path"] = path
	}
	if cwd, err := processCwd(pid); err == nil {
		result["cwd"] = cwd
	}
	if command, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		if argv0, _, _ := strings.Cut(string(command), "\x00"); argv0 != "" {
			result["argv0"] = argv0
			result["invoked_path"] = argv0
		}
	}
	return result, parent, nil
}
