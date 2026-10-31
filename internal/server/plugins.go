package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type pluginCreator struct {
	info    object
	process *blockPlugin
}
type remoteBlock struct {
	creator *pluginCreator
	id      string
	input   object
	bound   *blockPlugin
}
type pluginReply struct {
	payload any
	err     error
}

type blockPlugin struct {
	mu            sync.Mutex
	s             *Server
	cmd           *exec.Cmd
	reportedPID   int
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	wait          chan struct{}
	next          uint64
	pending       map[string]chan pluginReply
	events        *controlQueue
	contextEvents *controlQueue
	blocks        map[string]*block
}

func DefaultBlockDirs() []string {
	home := os.Getenv("XDG_DATA_HOME")
	if home == "" {
		path, _ := os.UserHomeDir()
		home = filepath.Join(path, ".local", "share")
	}
	dirs := os.Getenv("XDG_DATA_DIRS")
	if dirs == "" {
		dirs = "/usr/local/share:/usr/share"
	}
	result := []string{filepath.Join(home, "rex", "blocks")}
	for _, dir := range filepath.SplitList(dirs) {
		if dir != "" {
			result = append(result, filepath.Join(dir, "rex", "blocks"))
		}
	}
	return result
}

func (s *Server) LoadPlugins(dirs []string, reload ...bool) {
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			log.Printf("block plugins: %v", err)
			continue
		}
		for _, entry := range entries {
			if seen[entry.Name()] {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				continue
			}
			seen[entry.Name()] = true
			plugin, creators, err := s.startPlugin(path)
			if err != nil {
				log.Printf("block plugin %s: %v", path, err)
				continue
			}
			s.mu.Lock()
			s.plugins = append(s.plugins, plugin)
			for _, creator := range creators {
				name := text(creator, "name")
				if name == "" || name == "com.superlogical.terminal" || s.creators[name] != nil {
					continue
				}
				if text(creator, "title") == "" || text(creator, "description") == "" {
					log.Printf("block plugin %s: creator %q needs a title and description", path, name)
					continue
				}
				if text(creator, "host_id") == "" {
					creator["host_id"] = s.hostID
				}
				s.creators[name] = &pluginCreator{info: creator, process: plugin}
			}
			s.mu.Unlock()
			if len(reload) > 0 && reload[0] {
				s.pluginWorkers.Add(1)
				go s.watchPlugin(path, plugin)
			}
		}
	}
}

func (s *Server) startPlugin(path string) (*blockPlugin, []object, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer read.Close()
	ctx, cancel := context.WithCancel(s.lifetime)
	p := &blockPlugin{s: s, ctx: ctx, cancel: cancel, wait: make(chan struct{}), pending: map[string]chan pluginReply{}, events: newControlQueue(), contextEvents: newControlQueue(), blocks: map[string]*block{}}
	cmd := exec.Command(path)
	p.cmd = cmd
	cmd.Env = append(os.Environ(), "REX_BLOCKSERVER_INFO_FD=3")
	cmd.ExtraFiles = []*os.File{write}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		write.Close()
		cancel()
		return nil, nil, err
	}
	write.Close()
	go func() { cmd.Wait(); cancel(); close(p.wait) }()
	ready := false
	defer func() {
		if !ready {
			p.close()
		}
	}()
	infoCh := make(chan pluginReply, 1)
	go func() {
		var info object
		err := json.NewDecoder(io.LimitReader(read, 1<<20)).Decode(&info)
		infoCh <- pluginReply{info, err}
	}()
	startup, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var info object
	select {
	case reply := <-infoCh:
		if reply.err != nil {
			return nil, nil, reply.err
		}
		info = asObject(reply.payload)
	case <-startup.Done():
		return nil, nil, fmt.Errorf("plugin did not report a connection: %w", startup.Err())
	}
	reportedPID := number(info, "pid", 0)
	if reportedPID != cmd.Process.Pid && !descendsFrom(reportedPID, cmd.Process.Pid) {
		return nil, nil, fmt.Errorf("plugin reported the wrong process ID")
	}
	p.reportedPID = reportedPID
	network, address, base := text(info, "network"), text(info, "addr"), text(info, "base_url")
	if network != "unix" && network != "tcp" || address == "" || base == "" {
		return nil, nil, fmt.Errorf("plugin reported an invalid connection")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	base = strings.TrimRight(base, "/")
	url := strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/control"
	conn, _, err := websocket.Dial(startup, url, &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{controlProtocol}})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	p.conn = conn
	conn.SetReadLimit(8 << 20)
	if err := wsjson.Write(startup, conn, object{"type": "hello"}); err != nil {
		return nil, nil, err
	}
	var welcome object
	if err := wsjson.Read(startup, conn, &welcome); err != nil {
		return nil, nil, err
	}
	if welcome["type"] != "welcome" || welcome["protocol"] != controlProtocol {
		return nil, nil, fmt.Errorf("plugin reported an incompatible control protocol")
	}
	go p.readControl()
	go p.deliverEvents()
	go p.deliverContextEvents()
	value, err := p.call(startup, "block_creator.list", object{})
	if err != nil {
		return nil, nil, err
	}
	creators := []object{}
	if items, ok := asObject(value)["creators"].([]any); ok {
		for _, value := range items {
			creator := asObject(value)
			if err := normalizePluginCreator(creator); err != nil {
				return nil, nil, err
			}
			creators = append(creators, creator)
		}
	}
	ready = true
	return p, creators, nil
}

func normalizePluginCreator(creator object) error {
	name := text(creator, "name")
	if strings.TrimSpace(name) == "" || strings.TrimSpace(text(creator, "title")) == "" || strings.TrimSpace(text(creator, "description")) == "" {
		return fmt.Errorf("plugin creator needs name, title and description")
	}
	flavors, ok := creator["flavors"].([]any)
	if !ok || len(flavors) == 0 {
		return fmt.Errorf("plugin creator %q needs flavors", name)
	}
	seen := map[string]bool{}
	for _, value := range flavors {
		flavor := asObject(value)
		short := text(flavor, "name")
		if short == "" || seen[short] {
			return fmt.Errorf("plugin creator %q has an empty or repeated flavor", name)
		}
		seen[short] = true
		if text(flavor, "flavor") == "" {
			flavor["flavor"] = name + "." + short
		}
	}
	return nil
}

func (p *blockPlugin) call(ctx context.Context, method string, payload object) (any, error) {
	p.mu.Lock()
	p.next++
	id := fmt.Sprintf("control-%d", p.next)
	reply := make(chan pluginReply, 1)
	p.pending[id] = reply
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, p.conn, object{"type": "request", "id": id, "method": method, "payload": payload}); err != nil {
		return nil, err
	}
	select {
	case value := <-reply:
		return value.payload, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, fmt.Errorf("block plugin disconnected")
	}
}

func (p *blockPlugin) readControl() {
	defer p.cancel()
	defer p.conn.CloseNow()
	for {
		var frame object
		if err := wsjson.Read(p.ctx, p.conn, &frame); err != nil {
			return
		}
		switch frame["type"] {
		case "response":
			p.mu.Lock()
			reply := p.pending[text(frame, "id")]
			p.mu.Unlock()
			if reply == nil {
				continue
			}
			value := pluginReply{payload: frame["payload"]}
			if failure, ok := frame["error"].(map[string]any); ok {
				value.err = &apiError{text(failure, "code"), text(failure, "message"), number(failure, "status", 500)}
			}
			select {
			case reply <- value:
			default:
			}
		case "event":
			if !p.events.push(frame["payload"]) {
				return
			}
		}
	}
}

func (p *blockPlugin) deliverEvents() {
	for {
		data, ok := p.events.next(p.ctx)
		if !ok {
			return
		}
		var event object
		if json.Unmarshal(data, &event) != nil {
			p.cancel()
			return
		}
		payload := asObject(event["payload"])
		p.mu.Lock()
		b := p.blocks[text(payload, "block_id")]
		p.mu.Unlock()
		if b == nil {
			continue
		}
		p.s.mu.Lock()
		v, _ := p.s.findSession(b.sessionID)
		if p.s.closed || v == nil || v.blocks[b.id] != b || b.remote == nil || b.remote.bound != p || b.remote.creator.process != p {
			p.s.mu.Unlock()
			continue
		}
		switch event["type"] {
		case "block_event":
			p.s.sessionEvent(b.sessionID, "block_event", object{"block_id": b.id, "creator_name": b.creatorName(), "name": payload["name"], "payload": payload["payload"]})
		case "block_closed", "closed":
			b.close()
			v.removePlacement(b.id)
			v.dropBlock(b.id)
			p.s.sessionEvent(v.id, "block_closed", object{"block_id": b.id})
			v.changed(p.s)
		}
		p.s.mu.Unlock()
	}
}

func (p *blockPlugin) deliverContextEvents() {
	for {
		data, ok := p.contextEvents.next(p.ctx)
		if !ok {
			return
		}
		var payload object
		if json.Unmarshal(data, &payload) != nil {
			return
		}
		if _, err := p.call(p.ctx, "block.event", payload); err != nil && p.ctx.Err() == nil {
			log.Printf("block plugin event: %v", err)
		}
	}
}

func (p *blockPlugin) close() {
	p.cancel()
	if p.conn != nil {
		p.conn.CloseNow()
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGINT)
	if p.reportedPID > 0 && p.reportedPID != p.cmd.Process.Pid {
		syscall.Kill(p.reportedPID, syscall.SIGINT)
	}
	select {
	case <-p.wait:
	case <-time.After(2 * time.Second):
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.wait
	}
}

func (s *Server) pluginFlavor(flavor string) *pluginCreator {
	for _, creator := range s.creators {
		flavors, _ := creator.info["flavors"].([]any)
		for _, value := range flavors {
			if text(asObject(value), "flavor") == flavor {
				return creator
			}
		}
	}
	return nil
}

func (s *Server) newPluginBlock(sid string, input object) (*block, error) {
	creator := s.pluginFlavor(text(input, "flavor"))
	if creator == nil {
		return nil, bad("unsupported block flavor")
	}
	for _, value := range creator.info["flavors"].([]any) {
		flavor := asObject(value)
		if flavor["flavor"] == input["flavor"] {
			options := input["options"]
			if options == nil {
				options = object{}
			}
			if err := validateArgs(flavor["options_schema"], options); err != nil {
				return nil, bad(err.Error())
			}
		}
	}
	b := &block{s: s, id: id("block"), sessionID: sid, label: text(input, "label"), channels: map[*dataChannel]bool{}}
	args := cloneObject(input)
	delete(args, "label")
	args["session_id"] = sid
	args["block_id"] = b.id
	result, err := creator.process.call(creator.process.ctx, "block.create", args)
	if err != nil {
		return nil, err
	}
	remoteID := text(asObject(result), "block_id")
	if !validTypedID(remoteID, "block") {
		return nil, fmt.Errorf("plugin returned an invalid block ID")
	}
	b.remote = &remoteBlock{creator: creator, id: remoteID, input: cloneObject(input), bound: creator.process}
	if b.label == "" {
		b.label = text(creator.info, "title")
	}
	creator.process.mu.Lock()
	creator.process.blocks[remoteID] = b
	creator.process.mu.Unlock()
	_, err = creator.process.call(creator.process.ctx, "block.event", object{"block_id": remoteID, "event": object{"type": "session_attached", "payload": object{"block_id": b.id, "session_id": sid}}})
	if err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}

func (b *block) creatorName() string {
	if b.remote != nil {
		return text(b.remote.creator.info, "name")
	}
	return "com.superlogical.terminal"
}
func (b *block) flavorName() string {
	if b.remote != nil {
		return text(b.remote.input, "flavor")
	}
	return "com.superlogical.terminal.shell"
}
func (b *block) api() object {
	if b.remote != nil {
		return asObject(b.remote.creator.info["api"])
	}
	return terminalAPI()
}

func (v *session) notifyRemote(kind string, payload object) {
	for _, b := range v.blocks {
		if b.remote != nil && b.remote.bound == b.remote.creator.process && !b.remote.creator.process.contextEvents.push(object{"block_id": b.remote.id, "event": object{"type": kind, "payload": payload}}) {
			b.remote.creator.process.cancel()
		}
	}
}
