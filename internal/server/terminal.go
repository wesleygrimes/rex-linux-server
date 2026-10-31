package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	ghostty "go.mitchellh.com/libghostty"
)

type block struct {
	mu                                      sync.Mutex
	s                                       *Server
	id, sessionID, label, cwd, title, owner string
	titleOwner, pwdOwner                    object
	pwdReported                             bool
	file                                    *os.File
	writer                                  *ptyWriter
	cmd                                     *exec.Cmd
	term                                    *ghostty.Terminal
	mouse                                   *ghostty.MouseEncoder
	theme                                   *terminalTheme
	colorScheme                             ghostty.ColorScheme
	clipboard                               []clipboardWrite
	clipboardID                             uint64
	pending                                 []blockEvent
	programs                                map[string]programRecord
	programOrder                            uint64
	cols, rows, cellWidth, cellHeight       int
	channels                                map[*dataChannel]bool
	closed                                  bool
	desired                                 map[string]desiredSize
	resizeOrder                             uint64
	exit                                    *object
	outputDone                              chan struct{}
	progressReport                          *ghostty.TerminalProgressReport
	mutations                               []terminalMutation
	mutationTimer                           *time.Timer
	selectionGesture                        *ghostty.SelectionGesture
	selecting                               bool
	selectionX, selectionY                  int
	selectionMoved                          bool
	remote                                  *remoteBlock
}

func (s *Server) newBlock(sid string, input object) (*block, error) {
	if text(input, "flavor") != "com.superlogical.terminal.shell" {
		return s.newPluginBlock(sid, input)
	}
	options := asObject(input["options"])
	if err := validateArgs(shellOptionsSchema(), options); err != nil {
		return nil, bad("terminal options: " + err.Error())
	}
	cwd := text(options, "cwd")
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	if cwd == "" {
		cwd = "/"
	}
	size := asObject(options["size"])
	cols, rows := number(size, "columns", 80), number(size, "rows", 24)
	if cols < 1 || cols > 1000 || rows < 1 || rows > 1000 {
		return nil, bad("terminal dimensions must be between 1 and 1000")
	}
	b := &block{s: s, id: id("block"), sessionID: sid, label: text(input, "label"), cwd: cwd, cols: cols, rows: rows, cellWidth: 8, cellHeight: 16, channels: map[*dataChannel]bool{}}
	if b.label == "" {
		b.label = "shell"
	}
	cmd, err := shellCommand(s.shell, cwd, options)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Env, "REX_SESSION="+sid, "REX_BLOCK="+b.id)
	started := time.Now()
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	b.file, b.cmd = file, cmd
	b.writer = newPTYWriter(file)
	b.colorScheme = ghostty.ColorSchemeDark
	term, err := ghostty.NewTerminal(
		ghostty.WithSize(uint16(cols), uint16(rows)),
		ghostty.WithWritePty(func(_ *ghostty.Terminal, data []byte) { b.writer.Write(data) }),
		ghostty.WithColorScheme(func(_ *ghostty.Terminal) (ghostty.ColorScheme, bool) { return b.colorScheme, true }),
		ghostty.WithClipboardWrite(b.storeClipboard),
		ghostty.WithProgramStatus(b.applyProgramStatus),
		ghostty.WithSemanticPrompt(b.semanticPrompt),
		ghostty.WithTitleChanged(func(term *ghostty.Terminal) {
			b.title, _ = term.Title()
			b.titleOwner = b.currentOwner()
			b.pending = append(b.pending, blockEvent{"title_changed", b.titleValue()})
		}),
		ghostty.WithPwdChanged(func(term *ghostty.Terminal) {
			b.cwd, _ = term.Pwd()
			b.pwdReported = true
			b.pwdOwner = b.currentOwner()
			b.pending = append(b.pending, blockEvent{"pwd_changed", b.pwdValue()})
		}),
		ghostty.WithBell(func(_ *ghostty.Terminal) { b.pending = append(b.pending, blockEvent{"bell", object{}}) }),
		ghostty.WithDesktopNotification(func(_ *ghostty.Terminal, notification ghostty.TerminalDesktopNotification) {
			b.pending = append(b.pending, blockEvent{"desktop_notification", object{"title": notification.Title, "body": notification.Body}})
		}),
		ghostty.WithProgressReport(func(_ *ghostty.Terminal, report ghostty.TerminalProgressReport) {
			b.progressReport = &report
			state := map[ghostty.TerminalProgressState]string{ghostty.TerminalProgressStateRemove: "remove", ghostty.TerminalProgressStateSet: "set", ghostty.TerminalProgressStateError: "error", ghostty.TerminalProgressStateIndeterminate: "indeterminate", ghostty.TerminalProgressStatePause: "pause"}[report.State]
			payload := object{"state": state}
			if report.Progress >= 0 {
				payload["progress"] = report.Progress
			}
			b.pending = append(b.pending, blockEvent{"progress_report", payload})
			b.inferProgress(report)
		}),
	)
	if err != nil {
		b.writer.close()
		syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		file.Close()
		cmd.Wait()
		return nil, err
	}
	b.term = term
	if options["theme"] != nil {
		if _, err := b.setTheme(asObject(options["theme"])); err != nil {
			b.close()
			cmd.Wait()
			return nil, err
		}
	}
	b.outputDone = make(chan struct{})
	go b.readOutput()
	go b.monitorProcess()
	go func() {
		err := cmd.Wait()
		runtime := time.Since(started)
		select {
		case <-b.outputDone:
		case <-time.After(250 * time.Millisecond):
		}
		code := cmd.ProcessState.ExitCode()
		result := object{"exit_code": code}
		if err != nil {
			result["error"] = err.Error()
		}
		b.mu.Lock()
		b.exit = &result
		b.scheduleMutation("exit-marker", func() error {
			for channel := range b.channels {
				channel.append(b.childExitMarker(channel.nonce))
			}
			return nil
		})
		b.removeInProgress("exit")
		pending := b.pending
		b.pending = nil
		b.mu.Unlock()
		s.mu.Lock()
		v, _ := s.findSession(sid)
		if !s.closed && v != nil && v.blocks[b.id] == b {
			for _, event := range pending {
				b.event(event.name, event.payload)
			}
			s.sessionEvent(sid, "block_event", object{"block_id": b.id, "creator_name": "com.superlogical.terminal", "name": "child_exited", "payload": result})
			policy := asObject(options["exit"])
			onCompletion, present := policy["on_completion"].(bool)
			if !present {
				onCompletion = true
			}
			threshold := time.Duration(number(policy, "quick_exit_threshold_ms", 250)) * time.Millisecond
			if onCompletion && (err == nil || runtime > threshold) {
				b.close()
				v.removePlacement(b.id)
				v.dropBlock(b.id)
				s.sessionEvent(sid, "block_closed", object{"block_id": b.id})
				v.changed(s)
			}
		}
		s.mu.Unlock()
	}()
	if initial := text(options, "initial_input"); initial != "" {
		b.writer.Write([]byte(initial))
	}
	return b, nil
}

func (b *block) readOutput() {
	defer close(b.outputDone)
	buf := make([]byte, 32<<10)
	for {
		n, err := b.file.Read(buf)
		if n > 0 {
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				return
			}
			b.writeOutput(buf[:n])
			b.inferApplication()
			pending := b.pending
			b.pending = nil
			b.mu.Unlock()
			if len(pending) > 0 {
				b.s.mu.Lock()
				if !b.s.closed {
					for _, event := range pending {
						b.event(event.name, event.payload)
					}
				}
				b.s.mu.Unlock()
			}
		}
		if err != nil {
			return
		}
	}
}

// close ends the PTY process group. Merely detaching a client never calls it.
func (b *block) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if b.mutationTimer != nil {
		b.mutationTimer.Stop()
	}
	b.mutations = nil
	b.clipboard, b.pending = nil, nil
	if b.remote != nil {
		plugin := b.remote.bound
		plugin.mu.Lock()
		delete(plugin.blocks, b.remote.id)
		plugin.mu.Unlock()
		ctx, cancel := context.WithTimeout(plugin.ctx, 2*time.Second)
		defer cancel()
		plugin.call(ctx, "block.close", object{"block_id": b.remote.id})
		return
	}
	b.writer.close()
	for d := range b.channels {
		d.close()
	}
	if b.exit == nil {
		syscall.Kill(-b.cmd.Process.Pid, syscall.SIGHUP)
	}
	b.file.Close()
	if b.mouse != nil {
		b.mouse.Close()
	}
	if b.selectionGesture != nil {
		b.selectionGesture.Close(b.term)
	}
	b.term.Close()
}

func (b *block) event(name string, payload object) {
	b.s.sessionEvent(b.sessionID, "block_event", object{"block_id": b.id, "creator_name": b.creatorName(), "name": name, "payload": payload})
}

// Caller holds the server and block locks.
func (b *block) flushEvents() {
	for _, event := range b.pending {
		b.event(event.name, event.payload)
	}
	b.pending = nil
}
func (b *block) size() object {
	var owner any
	if b.owner != "" {
		owner = b.owner
	}
	return object{"columns": b.cols, "rows": b.rows, "cell_width_px": b.cellWidth, "cell_height_px": b.cellHeight, "owner": owner, "desired": b.desiredSizes()}
}
func (b *block) resizeMarker(nonce uint64) []byte {
	o := object{"type": "resize", "cols": b.cols, "rows": b.rows, "cell_width_px": b.cellWidth, "cell_height_px": b.cellHeight, "owner": nil}
	if b.owner != "" {
		o["owner"] = b.owner
	}
	data, _ := json.Marshal(o)
	return []byte(fmt.Sprintf("\x1b_rex1;%d;%s\x1b\\", nonce, data))
}

func (b *block) childExitMarker(nonce uint64) []byte {
	data, _ := json.Marshal(object{"type": "child_exited", "exit_code": (*b.exit)["exit_code"]})
	return []byte(fmt.Sprintf("\x1b_rex1;%d;%s\x1b\\", nonce, data))
}
func (b *block) method(cid, method string, value any) (any, error) {
	b.mu.Lock()
	defer func() { b.flushEvents(); b.mu.Unlock() }()
	if b.closed {
		return nil, missing("block is closed")
	}
	if b.remote != nil {
		if err := b.ensureRemote(); err != nil {
			return nil, err
		}
		definition := asObject(asObject(b.api()["methods"])[method])
		if len(definition) == 0 {
			return nil, &apiError{"method_not_found", "unknown plugin method " + method, 404}
		}
		schema := definition["input_schema"]
		if schema == nil {
			schema = object{"type": "object", "additionalProperties": false}
		}
		if err := validateArgs(schema, value); err != nil {
			return nil, bad(err.Error())
		}
		return b.remote.creator.process.call(b.remote.creator.process.ctx, "block.method", object{"block_id": b.remote.id, "method": method, "args": value})
	}
	definition := asObject(asObject(terminalAPI()["methods"])[method])
	if len(definition) > 0 {
		if err := validateArgs(definition["input_schema"], value); err != nil {
			return nil, bad(err.Error())
		}
	}
	a := asObject(value)
	switch method {
	case "write":
		n, err := b.writer.Write([]byte(text(a, "data")))
		return object{"bytes_written": n}, err
	case "resize":
		return b.resizeForClient(cid, a)
	case "size":
		return b.size(), nil
	case "title":
		return b.titleValue(), nil
	case "pwd":
		return b.pwdValue(), nil
	case "program_status":
		return b.programStatus(), nil
	case "process":
		return b.process(), nil
	case "list_dir":
		return b.listDir(a)
	case "clipboard_read":
		return b.readClipboard(a, time.Now())
	case "format":
		format := ghostty.FormatterFormatPlain
		if text(a, "format") == "vt" {
			format = ghostty.FormatterFormatVT
		}
		if text(a, "format") == "html" {
			format = ghostty.FormatterFormatHTML
		}
		trim, _ := a["trim"].(bool)
		unwrap, _ := a["unwrap"].(bool)
		options := []ghostty.FormatterOption{ghostty.WithFormatterFormat(format), ghostty.WithFormatterTrim(trim), ghostty.WithFormatterUnwrap(unwrap)}
		for key, option := range map[string]func(bool) ghostty.FormatterOption{
			"extra_charsets": ghostty.WithFormatterExtraCharsets, "extra_cursor": ghostty.WithFormatterExtraCursor,
			"extra_hyperlink": ghostty.WithFormatterExtraHyperlink, "extra_keyboard": ghostty.WithFormatterExtraKeyboard,
			"extra_kitty_keyboard": ghostty.WithFormatterExtraKittyKeyboard, "extra_modes": ghostty.WithFormatterExtraModes,
			"extra_palette": ghostty.WithFormatterExtraPalette, "extra_protection": ghostty.WithFormatterExtraProtection,
			"extra_pwd": ghostty.WithFormatterExtraPwd, "extra_scrolling_region": ghostty.WithFormatterExtraScrollingRegion,
			"extra_style": ghostty.WithFormatterExtraStyle, "extra_tabstops": ghostty.WithFormatterExtraTabstops,
		} {
			if value, ok := a[key].(bool); ok {
				options = append(options, option(value))
			}
		}
		f, err := ghostty.NewFormatter(b.term, options...)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		content, err := f.FormatString()
		return object{"content": content}, err
	case "clear", "reset":
		data := []byte("\x1b[H\x1b[2J\x1b[3J")
		if method == "reset" {
			data = []byte("\x18\x1bc")
		}
		err := b.scheduleMutation(method, func() error {
			b.term.VTWrite(data)
			for d := range b.channels {
				d.append(data)
			}
			return nil
		})
		for _, event := range b.pending {
			b.event(event.name, event.payload)
		}
		b.pending = nil
		return object{}, err
	case "set_theme":
		return b.setTheme(a)
	default:
		return nil, &apiError{"method_not_found", "terminal method not implemented: " + method, 404}
	}
}

func (b *block) currentOwner() object {
	pid := b.foregroundPID()
	if name := processName(pid); name != "" {
		return object{"pid": pid, "name": name}
	}
	return nil
}

func (b *block) titleValue() object {
	out := object{"title": b.title}
	if b.titleOwner != nil {
		out["owner"] = b.titleOwner
	}
	return out
}

func (b *block) pwdValue() object {
	if !b.pwdReported {
		return object{}
	}
	pwd := b.cwd
	if !strings.Contains(pwd, "://") {
		host, _ := os.Hostname()
		pwd = (&url.URL{Scheme: "file", Host: host, Path: pwd}).String()
	}
	out := object{"pwd": pwd}
	if b.pwdOwner != nil {
		out["owner"] = b.pwdOwner
	}
	return out
}

func (b *block) input(event object) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, missing("block is closed")
	}
	if b.remote != nil {
		if err := b.ensureRemote(); err != nil {
			return nil, err
		}
		kind := strings.TrimPrefix(text(event, "type"), "input.")
		if kind != "key" && kind != "paste" && kind != "focus" && kind != "mouse" && kind != "wheel" {
			return nil, bad("unsupported input event: " + text(event, "type"))
		}
		return b.remote.creator.process.call(b.remote.creator.process.ctx, "block.event", object{"block_id": b.remote.id, "event": event})
	}
	a := asObject(event["payload"])
	kind := strings.TrimPrefix(text(event, "type"), "input.")
	switch kind {
	case "key":
		key, err := ghostty.ParseKey(text(a, "key"))
		if err != nil {
			return nil, bad(err.Error())
		}
		mods, err := ghostty.ParseMods(text(a, "mods"))
		if err != nil {
			return nil, bad(err.Error())
		}
		consumed, err := ghostty.ParseMods(text(a, "consumed_mods"))
		if err != nil {
			return nil, bad(err.Error())
		}
		ev, err := ghostty.NewKeyEvent()
		if err != nil {
			return nil, err
		}
		defer ev.Close()
		ev.SetKey(key)
		ev.SetMods(mods)
		ev.SetConsumedMods(consumed)
		ev.SetUTF8(text(a, "text"))
		ev.SetUnshiftedCodepoint(rune(number(a, "unshifted_codepoint", 0)))
		action := ghostty.KeyActionPress
		if text(a, "action") == "release" {
			action = ghostty.KeyActionRelease
		}
		if text(a, "action") == "repeat" {
			action = ghostty.KeyActionRepeat
		}
		ev.SetAction(action)
		enc, err := ghostty.NewKeyEncoder()
		if err != nil {
			return nil, err
		}
		defer enc.Close()
		enc.SetOptFromTerminal(b.term)
		data, err := enc.Encode(ev)
		if err != nil {
			return nil, err
		}
		_, err = b.writer.Write(data)
		return object{}, err
	case "paste":
		bracketed, err := b.term.Mode(ghostty.ModeBracketedPaste)
		if err != nil {
			return nil, err
		}
		data, err := ghostty.PasteEncode([]byte(text(a, "text")), bracketed)
		if err != nil {
			return nil, err
		}
		_, err = b.writer.Write(data)
		return object{}, err
	case "focus":
		var focus ghostty.FocusEvent
		switch text(a, "action") {
		case "", "in":
			focus = ghostty.FocusGained
		case "out":
			focus = ghostty.FocusLost
		default:
			return nil, bad("unknown focus action")
		}
		enabled, err := b.term.Mode(ghostty.ModeFocusEvent)
		if err != nil || !enabled {
			return object{}, err
		}
		data, err := ghostty.FocusEncode(focus)
		if err != nil {
			return nil, err
		}
		_, err = b.writer.Write(data)
		return object{}, err
	case "mouse":
		return b.inputMouse(a)
	case "wheel":
		return b.inputWheel(a)
	default:
		return nil, bad("unsupported input event: " + text(event, "type"))
	}
}
