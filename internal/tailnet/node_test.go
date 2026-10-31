package tailnet

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

func TestOwnerAdmission(t *testing.T) {
	for _, tc := range []struct {
		name            string
		owner, caller   tailcfg.UserID
		tags            []string
		allow, admitted bool
	}{
		{"owner", 42, 42, nil, false, true},
		{"another_account", 42, 77, nil, false, false},
		{"unknown_owner", 0, 42, nil, false, false},
		{"tagged_node_is_not_a_human_owner", 42, 42, []string{"tag:build"}, false, false},
		{"identified_member", 42, 77, nil, true, true},
		{"identified_tagged_node", 42, 0, []string{"tag:build"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			who := &apitype.WhoIsResponse{Node: &tailcfg.Node{User: tc.caller, Name: "caller.tail.test.", Tags: tc.tags}, UserProfile: &tailcfg.UserProfile{ID: tc.caller, LoginName: "test@example.com"}}
			p, err := Identify(who, tc.owner, tc.allow, "100.1.2.3:4444")
			if (err == nil) != tc.admitted {
				t.Fatalf("admitted=%v, error=%v", tc.admitted, err)
			}
			if err == nil && (p.Kind != "tailscale" || p.LoginName != "test@example.com" || p.Local()) {
				t.Fatalf("incorrect principal: %+v", p)
			}
		})
	}
	if _, err := Identify(nil, 42, true, ""); err == nil {
		t.Fatal("unidentified caller was admitted")
	}
}

type fakeListener struct {
	done chan struct{}
	once sync.Once
}

func (l *fakeListener) Accept() (net.Conn, error) { <-l.done; return nil, net.ErrClosed }
func (l *fakeListener) Close() error              { l.once.Do(func() { close(l.done) }); return nil }
func (l *fakeListener) Addr() net.Addr            { return &net.UnixAddr{Name: "fake", Net: "unix"} }

type fakeWatcher struct {
	ctx    context.Context
	events chan struct{}
}

func (w *fakeWatcher) Next() (ipn.Notify, error) {
	select {
	case <-w.ctx.Done():
		return ipn.Notify{}, w.ctx.Err()
	case <-w.events:
		return ipn.Notify{}, nil
	}
}
func (w *fakeWatcher) Close() error { return nil }

type fakeBackend struct {
	mu                          sync.Mutex
	status                      *ipnstate.Status
	listeners                   []*fakeListener
	events                      chan struct{}
	closed, loggedOut, loggedIn bool
	logoutErr                   error
	hostname                    string
}

func (b *fakeBackend) Start() error { return nil }
func (b *fakeBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, l := range b.listeners {
		l.Close()
	}
	return nil
}
func (b *fakeBackend) Listen(string, string) (net.Listener, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, net.ErrClosed
	}
	l := &fakeListener{done: make(chan struct{})}
	b.listeners = append(b.listeners, l)
	return l, nil
}
func (b *fakeBackend) ListenTLS(a, c string) (net.Listener, error) { return b.Listen(a, c) }
func (b *fakeBackend) Status(context.Context) (*ipnstate.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status, nil
}
func (b *fakeBackend) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	return nil, errors.New("not configured")
}
func (b *fakeBackend) Login(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loggedIn = true
	return nil
}
func (b *fakeBackend) Logout(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.logoutErr != nil {
		return b.logoutErr
	}
	b.loggedOut = true
	return nil
}
func (b *fakeBackend) Rename(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hostname = name
	return nil
}
func (b *fakeBackend) Watch(ctx context.Context) (watcher, error) {
	return &fakeWatcher{ctx: ctx, events: b.events}, nil
}

func TestPersistentNodeLifecycle(t *testing.T) {
	dir := t.TempDir()
	n, err := New(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	b := &fakeBackend{status: &ipnstate.Status{BackendState: "NeedsLogin", AuthURL: "https://login.example.test/flow"}, events: make(chan struct{}, 1)}
	starts := 0
	n.newBackend = func(c Config, key string) backend {
		starts++
		if key != "one-use-secret" {
			t.Fatalf("incorrect enrollment key")
		}
		return b
	}
	if err := n.Enable(Config{Hostname: "rex-linux"}, "one-use-secret"); err != nil {
		t.Fatal(err)
	}
	if err := n.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.Status().State != "needs_login" || n.Status().AuthURL == "" {
		t.Fatalf("login status: %+v", n.Status())
	}
	if err := n.Enable(Config{Hostname: "ignored", AllowTailnetMembers: true}, "ignored-key"); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || n.Config().Hostname != "rex-linux" || !n.Status().AllowTailnetMembers {
		t.Fatalf("enable restarted or renamed an existing node")
	}
	if err := n.Rename(context.Background(), "renamed-rex"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tailscale.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || !strings.Contains(string(data), "renamed-rex") {
		t.Fatalf("persisted config contains a key or lost the rename")
	}
	b.logoutErr = errors.New("temporary logout failure")
	if err := n.Disable(context.Background(), true); err == nil {
		t.Fatal("logout failure ignored")
	}
	if n.Status().State == "disabled" {
		t.Fatal("failed logout disabled the node")
	}
	b.logoutErr = nil
	if err := n.Disable(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !b.closed || b.loggedOut || n.Status().State != "disabled" {
		t.Fatal("disable did not preserve the login")
	}
	restored, err := New(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Config().Enabled || restored.Config().Hostname != "renamed-rex" {
		t.Fatal("saved lifecycle configuration was lost")
	}
	n.Close()
	if err := n.Enable(Config{Hostname: "closed-rex"}, ""); err == nil {
		t.Fatal("a closed node could be restarted")
	}
}

func TestStatusStates(t *testing.T) {
	for backend, want := range map[string]string{"Running": "running", "NeedsLogin": "needs_login", "NeedsMachineAuth": "needs_machine_approval", "Starting": "starting"} {
		st := &ipnstate.Status{BackendState: backend, Self: &ipnstate.PeerStatus{DNSName: "rex.tail.test."}}
		got := publicStatus(st, false, "available")
		if got.State != want || got.HTTPS.URL != "https://rex.tail.test" {
			t.Fatalf("%s: %+v", backend, got)
		}
	}
}
