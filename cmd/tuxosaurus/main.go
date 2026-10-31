package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/identity"
	"github.com/wesleygrimes/tuxosaurus/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "setup" {
		return runSetup(args[1:])
	}
	flag := flag.NewFlagSet("tuxosaurus", flag.ContinueOnError)
	listen := flag.String("listen", "127.0.0.1:51433", "HTTP listen address, or unix:///path/to/socket")
	state := flag.String("state-dir", defaultStateDir(), "persistent server state directory")
	shell := flag.String("shell", "", "default shell (defaults to SHELL, then /bin/sh)")
	config := flag.String("config", "", "Lua configuration file (default ~/.config/rex/init.lua)")
	maxSessions := flag.Int("max-sessions", 64, "maximum live sessions (0 for no limit)")
	maxBlocks := flag.Int("max-blocks", 1024, "maximum live blocks (0 for no limit)")
	maxWindows := flag.Int("max-windows", 64, "maximum windows per session (0 for no limit)")
	maxPanes := flag.Int("max-panes", 64, "maximum placed blocks per window (0 for no limit)")
	socketMode := flag.String("socket-mode", "0600", "Unix socket permissions (owner and group bits only)")
	exitEmpty := flag.Bool("exit-empty", false, "stop when no sessions or control connections remain")
	anonymous := flag.Bool("allow-anonymous", false, "allow full access on a non-loopback HTTP address")
	embedded := flag.Bool("tailscale", false, "also serve the API as an embedded Tailscale node")
	hostname := flag.String("tailscale-hostname", "", "embedded Tailscale device name (default rex)")
	allowMembers := flag.Bool("tailscale-allow-tailnet-members", false, "admit all identified callers permitted by tailnet ACLs (default: enrolled account only)")
	authFile := flag.String("tailscale-auth-key-file", "", "enroll with a one-time auth key from FILE, or '-' for stdin")
	var extra, tags, blockDirs, origins []string
	flag.Func("websocket-origin-pattern", "allow browser WebSockets from this origin pattern (repeatable)", func(value string) error { origins = append(origins, value); return nil })
	flag.Func("block-dir", "discover executable block plugins in DIR (repeatable)", func(value string) error { blockDirs = append(blockDirs, value); return nil })
	reloadPlugins := flag.Bool("block-plugin-reload", false, "reload block plugins when their executables change")
	flag.Func("extra-listen", "additional Unix or HTTP endpoint (repeatable)", func(value string) error { extra = append(extra, value); return nil })
	flag.Func("tailscale-advertise-tag", "ACL tag to advertise during enrollment (repeatable)", func(value string) error { tags = append(tags, value); return nil })
	if len(args) >= 2 && args[0] == "server" && args[1] == "run" {
		args = args[2:]
	}
	if err := flag.Parse(args); err != nil {
		return err
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	mode, err := strconv.ParseUint(*socketMode, 8, 32)
	if err != nil || mode & ^uint64(0770) != 0 {
		return fmt.Errorf("socket-mode must contain only owner and group permission bits")
	}

	s, err := server.New(*state, *shell, *config)
	if err != nil {
		return err
	}
	defer s.Close()
	s.ConfigureRun(*exitEmpty, origins)
	if err := s.SetLimits(server.Limits{Sessions: *maxSessions, Blocks: *maxBlocks, Windows: *maxWindows, Panes: *maxPanes}); err != nil {
		return err
	}
	if len(blockDirs) == 0 {
		blockDirs = server.DefaultBlockDirs()
	}
	s.LoadPlugins(blockDirs, *reloadPlugins)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var servers []*http.Server
	var listeners []net.Listener
	for _, endpoint := range append([]string{*listen}, extra...) {
		address, network := strings.TrimPrefix(endpoint, "http://"), "tcp"
		if strings.HasPrefix(endpoint, "unix://") {
			network, address = "unix", strings.TrimPrefix(endpoint, "unix://")
			if err := os.MkdirAll(filepath.Dir(address), 0700); err != nil {
				return err
			}
		} else {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if !*anonymous && (ip == nil || !ip.IsLoopback()) {
				return fmt.Errorf("a non-loopback HTTP listener requires --allow-anonymous")
			}
		}
		listener, err := net.Listen(network, address)
		if err != nil {
			return err
		}
		defer listener.Close()
		h := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
		if network == "unix" {
			if err := os.Chmod(address, os.FileMode(mode)); err != nil {
				return err
			}
			defer os.Remove(address)
			h.Handler = s.LocalHandler()
			h.ConnContext = identity.ForUnixConn
		}
		servers = append(servers, h)
		listeners = append(listeners, listener)
		log.Printf("Rex compatible server listening on %s (%s)", listener.Addr(), network)
	}
	cfg := s.Tailnet().Config()
	if *embedded || cfg.Enabled {
		if flagWasSet(flag, "tailscale-allow-tailnet-members") {
			cfg.AllowTailnetMembers = *allowMembers
		}
		if *hostname != "" {
			cfg.Hostname = *hostname
		}
		if len(tags) > 0 {
			cfg.AdvertiseTags = tags
		}
		var key string
		if *authFile != "" {
			var data []byte
			if *authFile == "-" {
				data, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
			} else {
				data, err = os.ReadFile(*authFile)
			}
			if err != nil {
				return err
			}
			if len(data) > 65536 {
				return fmt.Errorf("auth key file is too large")
			}
			key = strings.TrimSpace(string(data))
		}
		if err := s.Tailnet().Enable(cfg, key); err != nil {
			return err
		}
	}
	errors := make(chan error, len(servers))
	for i, h := range servers {
		go func(h *http.Server, l net.Listener) { errors <- h.Serve(l) }(h, listeners[i])
	}
	var serveErr error
	if *exitEmpty {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if s.Empty() {
						stop()
						return
					}
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
	case err := <-errors:
		if err != nil && err != http.ErrServerClosed {
			serveErr = fmt.Errorf("HTTP listener stopped: %w", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, h := range servers {
		h.Shutdown(shutdown)
	}
	s.Close()
	return serveErr
}
