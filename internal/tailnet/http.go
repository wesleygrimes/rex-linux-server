package tailnet

import (
	"net"
	"net/http"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/identity"
)

func (n *Node) serveHTTP(g *generation, listener net.Listener) error {
	h := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	h.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := g.driver.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			http.Error(w, "Tailscale caller could not be identified", http.StatusForbidden)
			return
		}
		n.mu.Lock()
		current, owner, allow := n.active == g, g.owner, n.config.AllowTailnetMembers
		n.mu.Unlock()
		if !current {
			http.Error(w, "Tailscale is disabled", http.StatusServiceUnavailable)
			return
		}
		p, err := Identify(who, owner, allow, r.RemoteAddr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		n.handler.ServeHTTP(w, r.WithContext(identity.With(r.Context(), p)))
	})
	n.mu.Lock()
	if n.active != g {
		n.mu.Unlock()
		listener.Close()
		return nil
	}
	g.httpServers = append(g.httpServers, h)
	n.mu.Unlock()
	return h.Serve(listener)
}

func (n *Node) stop(g *generation) error {
	g.cancel()
	n.mu.Lock()
	servers := append([]*http.Server(nil), g.httpServers...)
	n.mu.Unlock()
	for _, server := range servers {
		server.Close()
	}
	return g.driver.Close()
}
