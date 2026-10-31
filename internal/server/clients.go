package server

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type clientReply struct {
	result any
	err    error
}

type clientCall struct {
	owner *client
	reply chan clientReply
}

type performRequest struct {
	ctx        context.Context
	requester  object
	args       object
	done       chan clientReply
	targetDone <-chan struct{}
}

func (s *Server) findClient(clientID string) *client {
	for _, c := range s.clients {
		if c.id == clientID {
			return c
		}
	}
	return nil
}

func (s *Server) clientInfo(c *client) object {
	state := "connected"
	out := object{"client_id": c.id, "info": maps.Clone(c.info), "principal": c.principal, "connected_at": c.connectedAt}
	if c.conn == nil {
		state = "lost"
		out["connection_lost_at"] = c.lostAt
	}
	out["connection_state"] = state
	sessions := []string{}
	for sid := range c.attached {
		if _, err := s.findSession(sid); err == nil {
			sessions = append(sessions, sid)
		}
	}
	if len(sessions) > 0 {
		slices.Sort(sessions)
		out["session_ids"] = sessions
	}
	return out
}

func (s *Server) inspectClient(a object) (any, error) {
	id := text(a, "client_id")
	if !validTypedID(id, "client") {
		return nil, bad("client_id must be a client ID")
	}
	c := s.findClient(id)
	if c == nil || c.conn == nil {
		return nil, missing("client is not connected")
	}
	out := object{"client": s.clientInfo(c)}
	if len(c.actions) > 0 {
		out["actions"] = c.actions
	}
	return out, nil
}

func (s *Server) emitClientLocked(c *client, kind string, payload any) {
	if c.conn == nil {
		return
	}
	c.seq++
	if !c.out.push(object{"type": "event", "seq": c.seq, "payload": object{"type": kind, "payload": payload}}) {
		c.conn.CloseNow()
	}
}

func (s *Server) replyClient(c *client, a object) (any, error) {
	callID := text(a, "call_id")
	_, hasResult := a["result"]
	hasError := a["error"] != nil
	if callID == "" || hasResult == hasError {
		return nil, bad("call_id and exactly one of result or error are required")
	}
	var err error
	if hasError {
		failure := asObject(a["error"])
		if text(failure, "code") == "" || text(failure, "message") == "" {
			return nil, bad("error needs code and message")
		}
		err = &apiError{text(failure, "code"), text(failure, "message"), 500}
	}
	call := s.pendingCalls[callID]
	if call == nil || call.owner != c {
		return nil, missing("no client call is waiting for this reply")
	}
	delete(s.pendingCalls, callID)
	call.reply <- clientReply{result: a["result"], err: err}
	return object{}, nil
}

func (s *Server) performClient(ctx context.Context, requester *client, a object) (any, error) {
	s.mu.Lock()
	clientID, action := text(a, "client_id"), text(a, "action")
	if !validTypedID(clientID, "client") || action == "" || number(a, "timeout_ms", 0) < 0 {
		s.mu.Unlock()
		return nil, bad("client_id, action and a non-negative timeout_ms are required")
	}
	if args, present := a["args"]; present && args != nil {
		if _, ok := args.(map[string]any); !ok {
			s.mu.Unlock()
			return nil, bad("args must be an object")
		}
	}
	target := s.findClient(clientID)
	if target == nil {
		s.mu.Unlock()
		return nil, missing("client not found")
	}
	requesterInfo := object{"client_id": requester.id, "info": maps.Clone(requester.info), "principal": requester.principal}
	if len(target.actions) > 0 {
		known := false
		for _, declared := range target.actions {
			if text(asObject(declared), "name") == action {
				known = true
				args := a["args"]
				if args == nil {
					args = object{}
				}
				if err := validateArgs(asObject(declared)["args"], args); err != nil {
					s.reportPerformedLocked(target, requesterInfo, a, bad(err.Error()))
					s.mu.Unlock()
					return nil, bad(err.Error())
				}
				break
			}
		}
		if !known {
			s.reportPerformedLocked(target, requesterInfo, a, &apiError{"not_supported", "client does not declare action " + action, 501})
			s.mu.Unlock()
			return nil, &apiError{"not_supported", "client does not declare action " + action, 501}
		}
	}
	if target.conn == nil || len(target.performQueue) >= 16 {
		s.reportPerformedLocked(target, requesterInfo, a, &apiError{"client_unavailable", "client is reconnecting or its action queue is full", 503})
		s.mu.Unlock()
		return nil, &apiError{"client_unavailable", "client is reconnecting or its action queue is full", 503}
	}
	request := &performRequest{ctx: ctx, args: maps.Clone(a), done: make(chan clientReply, 1), targetDone: target.done,
		requester: requesterInfo}
	target.performQueue = append(target.performQueue, request)
	if !target.performing {
		target.performing = true
		go s.runPerformQueue(target)
	}
	s.mu.Unlock()
	select {
	case reply := <-request.done:
		if reply.err != nil {
			return nil, reply.err
		}
		out := object{"performed_by": target.id}
		if reply.result != nil {
			out["result"] = reply.result
		}
		return out, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) runPerformQueue(target *client) {
	for {
		s.mu.Lock()
		if len(target.performQueue) == 0 {
			target.performing = false
			s.mu.Unlock()
			return
		}
		request := target.performQueue[0]
		target.performQueue = target.performQueue[1:]
		callID := secret()
		call := &clientCall{owner: target, reply: make(chan clientReply, 1)}
		payload := object{"call_id": callID, "action": request.args["action"], "requester": request.requester}
		for _, field := range []string{"args", "origin", "session_id", "block_id"} {
			if value, present := request.args[field]; present {
				payload[field] = value
			}
		}
		connected := target.conn != nil && target.done == request.targetDone
		if connected && request.ctx.Err() == nil {
			s.pendingCalls[callID] = call
			s.emitClientLocked(target, "client_perform_requested", payload)
		}
		s.mu.Unlock()
		reply := clientReply{}
		if !connected {
			reply.err = &apiError{"client_unavailable", "client disconnected", 503}
		} else {
			limit := number(request.args, "timeout_ms", 0)
			if limit == 0 {
				limit = 10000
			}
			timer := time.NewTimer(time.Duration(min(limit, 30000)) * time.Millisecond)
			select {
			case reply = <-call.reply:
			case <-request.ctx.Done():
				reply.err = request.ctx.Err()
			case <-request.targetDone:
				reply.err = &apiError{"client_unavailable", "client disconnected", 503}
			case <-timer.C:
				reply.err = &apiError{"timeout", "client did not reply in time", 504}
			}
			timer.Stop()
		}
		s.mu.Lock()
		delete(s.pendingCalls, callID)
		s.reportPerformedLocked(target, request.requester, request.args, reply.err)
		s.mu.Unlock()
		request.done <- reply
	}
}

func (s *Server) reportPerformedLocked(target *client, requester, args object, err error) {
	outcome := "performed"
	report := object{"client_id": target.id, "action": args["action"], "requester": requester}
	if err != nil {
		outcome = "failed"
		failure, ok := err.(*apiError)
		if !ok {
			failure = &apiError{"internal", fmt.Sprint(err), 500}
		}
		switch failure.Code {
		case "bad_request", "not_supported", "refused":
			outcome = "refused"
		case "timeout":
			outcome = "timeout"
		}
		report["error"] = object{"code": failure.Code, "message": failure.Message}
	}
	report["outcome"] = outcome
	s.clientEventLocked("client_performed", report)
}

func (s *Server) clientEventLocked(kind string, payload object) {
	for _, observer := range s.clients {
		if observer.all {
			s.emitClientLocked(observer, kind, payload)
		}
	}
}

func (s *Server) expireClient(c *client, lostAt time.Time) {
	s.mu.Lock()
	if c.conn != nil || c.lostAt != lostAt || s.closed || s.clients[c.token] != c {
		s.mu.Unlock()
		return
	}
	delete(s.clients, c.token)
	for sid := range c.attached {
		if v, err := s.findSession(sid); err == nil {
			v.notifyRemote("client_disconnected", object{"client_id": c.id})
		}
	}
	s.clientEventLocked("client_left", object{"client_id": c.id})
	vm := c.script
	if vm != nil {
		vm.cancel()
	}
	s.mu.Unlock()
	if vm != nil {
		vm.close()
	}
}

func (s *Server) attachClient(c *client, v *session) {
	if c.attached[v.id] {
		return
	}
	c.attached[v.id] = true
	v.notifyRemote("client_connected", object{"client_id": c.id})
	s.clientEventLocked("client_changed", object{"client": s.clientInfo(c)})
}

func (s *Server) listClients(a object) (any, error) {
	kinds, ok := a["kinds"].([]any)
	if !ok && a["kinds"] != nil {
		return nil, bad("kinds must be an array")
	}
	clients := []any{}
	for _, c := range s.clients {
		if c.conn == nil && time.Since(c.lostAt) > 60*time.Second {
			continue
		}
		if sid := text(a, "session_id"); sid != "" && !c.attached[sid] {
			continue
		}
		if len(kinds) > 0 && !slices.Contains(kinds, any(text(c.info, "kind"))) {
			continue
		}
		clients = append(clients, s.clientInfo(c))
	}
	slices.SortFunc(clients, func(a, b any) int {
		return strings.Compare(text(asObject(a), "client_id"), text(asObject(b), "client_id"))
	})
	return object{"clients": clients}, nil
}

func clientLabel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		return "", bad("client label must be at most 64 bytes")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return "", bad("client label cannot contain control characters")
	}
	return value, nil
}

func sanitizeClientInfo(info object) object {
	info = maps.Clone(info)
	if info == nil {
		info = object{}
	}
	label := strings.TrimSpace(text(info, "label"))
	if strings.ContainsFunc(label, unicode.IsControl) {
		label = ""
	}
	for len(label) > 64 {
		_, size := utf8.DecodeLastRuneInString(label)
		label = label[:len(label)-size]
	}
	if label == "" {
		delete(info, "label")
	} else {
		info["label"] = label
	}
	return info
}
