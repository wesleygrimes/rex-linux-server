package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"rex-linux-server/internal/identity"
	"rex-linux-server/internal/tailnet"
)

// Unix socket access is protected by its filesystem permissions. Only that
// listener can administer the embedded node; HTTP loopback is not sufficient.
func (s *Server) LocalHandler() http.Handler {
	h := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !identity.From(r).Local() {
			http.Error(w, "Unix peer credentials are required", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) tailnetChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	status := s.tailnet.Status()
	if status.State == "disabled" {
		for _, c := range s.clients {
			if c.principal.Kind == "tailscale" && c.conn != nil {
				c.conn.CloseNow()
			}
		}
		for _, d := range s.channels {
			if d.principal.Kind == "tailscale" {
				d.close()
			}
		}
	}
	s.emitLocked("tailscale_status_changed", status, "")
}

func (s *Server) tailnetRequest(c *client, method string, a object) (any, error) {
	if !c.principal.Local() {
		return nil, &apiError{"forbidden", "this method requires the local Unix socket", http.StatusForbidden}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	switch strings.TrimPrefix(method, "server.tailscale.") {
	case "enable":
		allow, _ := a["allow_tailnet_members"].(bool)
		cfg := tailnet.Config{Hostname: text(a, "hostname"), AllowTailnetMembers: allow}
		if tags, ok := a["advertise_tags"].([]any); ok {
			for _, tag := range tags {
				t, ok := tag.(string)
				if !ok {
					return nil, bad("advertise_tags must contain strings")
				}
				cfg.AdvertiseTags = append(cfg.AdvertiseTags, t)
			}
		}
		if err := tailnet.Validate(cfg); err != nil {
			return nil, bad(err.Error())
		}
		err = s.tailnet.Enable(cfg, text(a, "auth_key"))
	case "disable":
		err = s.tailnet.Disable(ctx, false)
	case "logout":
		err = s.tailnet.Disable(ctx, true)
	case "login":
		err = s.tailnet.Login(ctx)
	case "rename":
		err = s.tailnet.Rename(ctx, text(a, "hostname"))
	default:
		return nil, &apiError{"method_not_found", "unknown Tailscale method", 404}
	}
	if err != nil {
		return nil, bad(err.Error())
	}
	return object{"tailscale": s.tailnet.Status()}, nil
}
