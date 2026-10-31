package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"rex-linux-server/internal/identity"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

type Config struct {
	Enabled             bool     `json:"enabled"`
	Hostname            string   `json:"hostname"`
	AllowTailnetMembers bool     `json:"allow_tailnet_members"`
	AdvertiseTags       []string `json:"advertise_tags,omitempty"`
}
type HTTPSStatus struct {
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}
type Account struct {
	LoginName string `json:"login_name"`
}
type Status struct {
	Error               string       `json:"error,omitempty"`
	State               string       `json:"state"`
	DNSName             string       `json:"dns_name,omitempty"`
	AuthURL             string       `json:"auth_url,omitempty"`
	Account             *Account     `json:"account,omitempty"`
	TailnetName         string       `json:"tailnet_name,omitempty"`
	AllowTailnetMembers bool         `json:"allow_tailnet_members"`
	Tags                []string     `json:"tags,omitempty"`
	HTTPS               *HTTPSStatus `json:"https,omitempty"`
}

type generation struct {
	driver      backend
	ctx         context.Context
	cancel      context.CancelFunc
	httpServers []*http.Server
	owner       tailcfg.UserID
	https       string
}

type Node struct {
	op         sync.Mutex
	mu         sync.Mutex
	dir        string
	config     Config
	status     Status
	active     *generation
	handler    http.Handler
	changes    chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
	closed     bool
	userLogf   func(string, ...any)
	newBackend func(Config, string) backend
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func Validate(c Config) error {
	if c.Hostname != "" && !hostnamePattern.MatchString(c.Hostname) {
		return errors.New("hostname must be one DNS label of 1 to 63 letters, digits or hyphens")
	}
	for _, tag := range c.AdvertiseTags {
		if err := tailcfg.CheckTag(tag); err != nil {
			return err
		}
	}
	return nil
}

type Option func(*Node)

func WithUserLogf(logf func(string, ...any)) Option {
	return func(n *Node) { n.userLogf = logf }
}

func New(dir string, handler http.Handler, changed func(), options ...Option) (*Node, error) {
	n := &Node{dir: dir, handler: handler, status: Status{State: "disabled"}, changes: make(chan struct{}, 1), done: make(chan struct{}), userLogf: log.Printf}
	for _, option := range options {
		option(n)
	}
	if dir != "" {
		data, err := os.ReadFile(filepath.Join(dir, "tailscale.json"))
		if err == nil {
			if err := json.Unmarshal(data, &n.config); err != nil {
				return nil, err
			}
			if err := Validate(n.config); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	n.newBackend = func(c Config, key string) backend {
		return &nativeBackend{Server: &tsnet.Server{Dir: filepath.Join(dir, "tailscale"), Hostname: c.Hostname, AuthKey: key, AdvertiseTags: c.AdvertiseTags, Logf: func(string, ...any) {}, UserLogf: n.userLogf}}
	}
	go func() {
		for {
			select {
			case <-n.changes:
				if changed != nil {
					changed()
				}
			case <-n.done:
				return
			}
		}
	}()
	return n, nil
}

func (n *Node) Config() Config {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.config
	c.AdvertiseTags = append([]string(nil), c.AdvertiseTags...)
	return c
}
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.status
	s.Tags = append([]string(nil), s.Tags...)
	return s
}
func (n *Node) publishLocked(s Status) {
	if reflect.DeepEqual(s, n.status) {
		return
	}
	n.status = s
	select {
	case n.changes <- struct{}{}:
	default:
	}
}
func (n *Node) save(c Config) error {
	if n.dir == "" {
		return errors.New("embedded Tailscale requires a state directory")
	}
	if err := os.MkdirAll(n.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(n.dir, "tailscale.json")
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (n *Node) Enable(c Config, authKey string) error {
	n.op.Lock()
	defer n.op.Unlock()
	if err := Validate(c); err != nil {
		return err
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return errors.New("Tailscale node is closed")
	}
	if n.active != nil {
		updated := n.config
		updated.AllowTailnetMembers = c.AllowTailnetMembers
		n.mu.Unlock()
		if err := n.save(updated); err != nil {
			return err
		}
		n.mu.Lock()
		n.config = updated
		s := n.status
		s.AllowTailnetMembers = c.AllowTailnetMembers
		n.publishLocked(s)
		n.mu.Unlock()
		return nil
	}
	if c.Hostname == "" {
		c.Hostname = n.config.Hostname
	}
	if c.Hostname == "" {
		c.Hostname = "rex"
	}
	n.mu.Unlock()
	c.Enabled = true
	if err := n.save(c); err != nil {
		return err
	}
	driver := n.newBackend(c, authKey)
	if err := driver.Start(); err != nil {
		driver.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &generation{driver: driver, ctx: ctx, cancel: cancel, https: "pending"}
	n.mu.Lock()
	n.config = c
	n.active = g
	n.publishLocked(Status{State: "starting", AllowTailnetMembers: c.AllowTailnetMembers, HTTPS: &HTTPSStatus{State: "pending"}})
	n.mu.Unlock()
	go n.watch(g)
	go n.serve(g, false)
	go n.serve(g, true)
	return nil
}

func (n *Node) refresh(g *generation) {
	ctx, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	st, err := g.driver.Status(ctx)
	if err != nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.active != g {
		return
	}
	if st.Self != nil {
		g.owner = st.Self.UserID
	}
	s := publicStatus(st, n.config.AllowTailnetMembers, g.https)
	n.publishLocked(s)
}
func publicStatus(st *ipnstate.Status, allow bool, https string) Status {
	s := Status{State: "starting", AllowTailnetMembers: allow, HTTPS: &HTTPSStatus{State: https}}
	switch st.BackendState {
	case "Running":
		s.State = "running"
	case "NeedsLogin":
		s.State = "needs_login"
	case "NeedsMachineAuth":
		s.State = "needs_machine_approval"
	}
	s.AuthURL = st.AuthURL
	if st.Self != nil {
		s.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		if u := st.User[st.Self.UserID]; u.LoginName != "" {
			s.Account = &Account{LoginName: u.LoginName}
		}
		if st.Self.Tags != nil {
			s.Tags = append([]string(nil), st.Self.Tags.AsSlice()...)
		}
	}
	if st.CurrentTailnet != nil {
		s.TailnetName = st.CurrentTailnet.Name
	}
	if s.DNSName != "" && https == "available" {
		s.HTTPS.URL = "https://" + s.DNSName
	}
	return s
}
func (n *Node) watch(g *generation) {
	for g.ctx.Err() == nil {
		n.refresh(g)
		w, err := g.driver.Watch(g.ctx)
		if err == nil {
			for {
				notification, nextErr := w.Next()
				err = nextErr
				if err != nil {
					break
				}
				n.refresh(g)
				if notification.ErrMessage != nil {
					n.mu.Lock()
					if n.active == g {
						s := n.status
						s.Error = *notification.ErrMessage
						n.publishLocked(s)
					}
					n.mu.Unlock()
				}
			}
			w.Close()
		}
		select {
		case <-g.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (n *Node) serve(g *generation, secure bool) {
	var err error
	if secure {
		l, listenErr := g.driver.ListenTLS("tcp", ":443")
		err = listenErr
		if err == nil {
			n.mu.Lock()
			if n.active != g {
				n.mu.Unlock()
				l.Close()
				return
			}
			g.https = "available"
			n.mu.Unlock()
			n.refresh(g)
			err = n.serveHTTP(g, l)
		}
	} else {
		l, listenErr := g.driver.Listen("tcp", ":80")
		err = listenErr
		if err == nil {
			err = n.serveHTTP(g, l)
		}
	}
	if err != nil && g.ctx.Err() == nil {
		log.Printf("embedded Tailscale listener: %v", err)
		if secure {
			n.mu.Lock()
			if n.active == g {
				g.https = "unavailable"
			}
			n.mu.Unlock()
			n.refresh(g)
		}
	}
}

func (n *Node) Login(ctx context.Context) error {
	n.op.Lock()
	defer n.op.Unlock()
	n.mu.Lock()
	g := n.active
	n.mu.Unlock()
	if g == nil {
		return errors.New("Tailscale is not enabled")
	}
	if err := g.driver.Login(ctx); err != nil {
		return err
	}
	n.refresh(g)
	return nil
}
func (n *Node) Rename(ctx context.Context, hostname string) error {
	if err := Validate(Config{Hostname: hostname}); err != nil {
		return err
	}
	if hostname == "" {
		return errors.New("hostname is required")
	}
	n.op.Lock()
	defer n.op.Unlock()
	n.mu.Lock()
	g, c := n.active, n.config
	n.mu.Unlock()
	if g == nil {
		return errors.New("Tailscale is not enabled")
	}
	if err := g.driver.Rename(ctx, hostname); err != nil {
		return err
	}
	c.Hostname = hostname
	if err := n.save(c); err != nil {
		return err
	}
	n.mu.Lock()
	n.config = c
	n.mu.Unlock()
	n.refresh(g)
	return nil
}
func (n *Node) Disable(ctx context.Context, logout bool) error {
	n.op.Lock()
	defer n.op.Unlock()
	n.mu.Lock()
	g, c := n.active, n.config
	n.mu.Unlock()
	if g == nil {
		return nil
	}
	if logout {
		if err := g.driver.Logout(ctx); err != nil {
			return err
		}
	}
	c.Enabled = false
	if err := n.save(c); err != nil {
		return err
	}
	n.mu.Lock()
	n.config = c
	n.active = nil
	n.publishLocked(Status{State: "disabled", AllowTailnetMembers: c.AllowTailnetMembers})
	n.mu.Unlock()
	return n.stop(g)
}
func (n *Node) Close() {
	n.closeOnce.Do(func() {
		n.op.Lock()
		defer n.op.Unlock()
		n.mu.Lock()
		n.closed = true
		g := n.active
		n.active = nil
		n.mu.Unlock()
		if g != nil {
			n.stop(g)
		}
		close(n.done)
	})
}

// Identify derives permissions from tsnet's peer identity. Owner-only mode
// rejects tagged nodes and peers belonging to another account.
func Identify(w *apitype.WhoIsResponse, owner tailcfg.UserID, allow bool, addr string) (identity.Principal, error) {
	if w == nil || w.Node == nil {
		return identity.Principal{}, errors.New("Tailscale caller could not be identified")
	}
	if !allow && (owner == 0 || w.Node.User != owner || len(w.Node.Tags) != 0) {
		return identity.Principal{}, errors.New("Tailscale caller is not the account that enrolled this server")
	}
	nodeName, _, _ := strings.Cut(w.Node.Name, ".")
	p := identity.Principal{Kind: "tailscale", Transport: "tailscale", RemoteAddr: addr, Node: nodeName, Tags: append([]string(nil), w.Node.Tags...), Path: []string{"tailscale"}}
	if w.UserProfile != nil {
		p.UserID = fmt.Sprint(w.UserProfile.ID)
		p.LoginName = w.UserProfile.LoginName
		p.Display = p.LoginName
	}
	if p.Display == "" {
		p.Display = p.Node
	}
	return p, nil
}
