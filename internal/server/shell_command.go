package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func resolveCommand(name, cwd, path string) (string, error) {
	if strings.ContainsRune(name, '/') {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && syscall.Access(candidate, 1) == nil {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func shellCommand(defaultShell, cwd string, options object) (*exec.Cmd, error) {
	shell := strings.TrimSpace(text(options, "shell"))
	if shell == "" {
		shell = defaultShell
	}
	path := os.Getenv("PATH")
	if paths := stringList(options["path_prepend"]); len(paths) > 0 {
		path = strings.Join(append(paths, path), string(os.PathListSeparator))
	}
	command := stringList(options["command"])
	login, present := options["login"].(bool)
	if !present {
		login = true
	}
	var cmd *exec.Cmd
	if shell == "none" && len(command) > 0 {
		name, err := resolveCommand(command[0], cwd, path)
		if err != nil {
			return nil, err
		}
		cmd = exec.Command(name, command[1:]...)
	} else {
		name, err := resolveCommand(shell, cwd, path)
		if err != nil {
			return nil, err
		}
		if len(command) > 0 {
			flag := "-c"
			if login {
				flag = "-lc"
			}
			switch filepath.Base(name) {
			case "fish":
				cmd = exec.Command(name, append([]string{flag, "exec $argv"}, command...)...)
			case "sh", "bash", "dash", "ash", "ksh", "zsh":
				cmd = exec.Command(name, append([]string{flag, `exec "$@"`, "rex-run"}, command...)...)
			default:
				executable, err := resolveCommand(command[0], cwd, path)
				if err != nil {
					return nil, err
				}
				cmd = exec.Command(executable, command[1:]...)
			}
		} else {
			cmd = exec.Command(name, stringList(options["args"])...)
			if login {
				cmd.Args[0] = "-" + filepath.Base(name)
			}
		}
	}
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PATH="+path, "PWD="+cwd, "TERM=xterm-256color", "TERM_PROGRAM=rex", "COLORTERM=truecolor")
	return cmd, nil
}
