package identity

import (
	"context"
	"github.com/tailscale/peercred"
	"net"
	"net/http"
	"strconv"
)

// Principal identifies the transport-authenticated caller. Client-supplied
// metadata never establishes identity or permission to administer the server.
type Principal struct {
	Kind       string   `json:"kind"`
	Transport  string   `json:"transport"`
	Display    string   `json:"display"`
	RemoteAddr string   `json:"remote_addr,omitempty"`
	UID        *int     `json:"uid,omitempty"`
	GID        *int     `json:"gid,omitempty"`
	PID        *int     `json:"pid,omitempty"`
	UserID     string   `json:"user_id,omitempty"`
	LoginName  string   `json:"login_name,omitempty"`
	Node       string   `json:"node,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Path       []string `json:"path,omitempty"`
}

type contextKey struct{}

func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func From(r *http.Request) Principal {
	if p, ok := r.Context().Value(contextKey{}).(Principal); ok {
		return p
	}
	return Principal{Kind: "anonymous", Transport: "http", Display: "anonymous", RemoteAddr: r.RemoteAddr}
}

func (p Principal) Local() bool { return p.Kind == "peer" && p.Transport == "unix" }

func ForUnixConn(ctx context.Context, conn net.Conn) context.Context {
	cred, err := peercred.Get(conn)
	if err != nil {
		return ctx
	}
	uidText, ok := cred.UserID()
	if !ok {
		return ctx
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil {
		return ctx
	}
	p := Principal{Kind: "peer", Transport: "unix", Display: "uid " + uidText, UID: &uid, GID: peerGroup(conn)}
	if pid, ok := cred.PID(); ok {
		p.PID = &pid
	}
	return With(ctx, p)
}

// Key binds reconnect credentials to an authenticated account, without binding
// them to an ephemeral source port or accepting a client-provided identifier.
func (p Principal) Key() string {
	key := p.Kind + ":" + p.Transport + ":" + p.UserID
	if p.UID != nil {
		key += ":" + strconv.Itoa(*p.UID)
	}
	return key
}
