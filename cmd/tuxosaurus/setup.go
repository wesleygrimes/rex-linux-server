package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/state"
	"github.com/wesleygrimes/tuxosaurus/internal/tailnet"
)

func defaultStateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "tuxosaurus")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "tuxosaurus")
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

func runSetup(args []string) error {
	flags := flag.NewFlagSet("tuxosaurus setup", flag.ContinueOnError)
	dir := flags.String("state-dir", defaultStateDir(), "state directory used by the server service")
	hostname := flags.String("tailscale-hostname", "", "server name (default tuxosaurus, or the saved name)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected setup arguments: %v", flags.Args())
	}
	unlock, err := state.Lock(*dir)
	if err != nil {
		return err
	}
	defer unlock()
	node, err := tailnet.New(*dir, http.NotFoundHandler(), nil, tailnet.WithUserLogf(func(string, ...any) {}))
	if err != nil {
		return err
	}
	defer node.Close()
	config := node.Config()
	if *hostname != "" {
		config.Hostname = *hostname
	} else if config.Hostname == "" {
		config.Hostname = "tuxosaurus"
	}
	if err := node.Enable(config, ""); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Setting up %s. Your browser can be on another device.\n", config.Hostname)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	url, err := waitForEnrollment(ctx, node.Status, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "\nSetup complete. Add this address in Rex:\n  %s\n\nStart the server service to accept connections. Future starts reuse this sign-in.\n", url)
	return nil
}

func waitForEnrollment(ctx context.Context, status func() tailnet.Status, out io.Writer) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	lastURL, lastState := "", ""
	for {
		s := status()
		if s.Error != "" {
			return "", errors.New(s.Error)
		}
		if s.AuthURL != "" && s.AuthURL != lastURL {
			fmt.Fprintf(out, "\nAuthorize this Rex server in Tailscale:\n  %s\n\nWaiting for authorization...\n", s.AuthURL)
			lastURL = s.AuthURL
		}
		if s.State == "needs_machine_approval" && s.State != lastState {
			fmt.Fprintln(out, "Waiting for a Tailscale administrator to approve this device.")
		}
		lastState = s.State
		if s.State == "running" && s.HTTPS != nil {
			switch s.HTTPS.State {
			case "available":
				if s.HTTPS.URL != "" {
					return s.HTTPS.URL, nil
				}
			case "unavailable":
				return "", errors.New("Tailscale is connected, but HTTPS is unavailable; check the error above and HTTPS certificates in your tailnet's DNS settings, then run setup again")
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
