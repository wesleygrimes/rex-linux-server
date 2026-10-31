package server

import (
	"os"
	"os/user"
	"reflect"
	"strconv"
	"sync"
	"time"
)

var processUsers sync.Map

func descendsFrom(pid, parent int) bool {
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] && len(seen) < 64 {
		if pid == parent {
			return true
		}
		seen[pid] = true
		_, next, err := processDetails(pid)
		if err != nil {
			return false
		}
		pid = next
	}
	return false
}

func processInfo(pid int) (object, int) {
	info, parent, err := processDetails(pid)
	if err != nil {
		return nil, 0
	}
	uid := strconv.Itoa(info["user_id"].(int))
	name, found := processUsers.Load(uid)
	if !found {
		value, err := user.LookupId(uid)
		name = ""
		if err == nil {
			name = value.Username
		}
		processUsers.Store(uid, name)
	}
	if name != "" {
		info["user"] = name
	}
	return info, parent
}

func (b *block) foregroundInfo() (object, []any) {
	pid := b.foregroundPID()
	foreground, parent := processInfo(pid)
	ancestors := []any{}
	seen := map[int]bool{pid: true}
	for parent > 1 && pid != b.cmd.Process.Pid && !seen[parent] && len(ancestors) < 32 {
		seen[parent] = true
		info, next := processInfo(parent)
		if info == nil {
			break
		}
		ancestors = append(ancestors, info)
		pid, parent = parent, next
	}
	return foreground, ancestors
}

func (b *block) process() object {
	home, _ := os.UserHomeDir()
	out := object{"last_exit": b.exit, "home": home}
	if b.exit == nil {
		if child, _ := processInfo(b.cmd.Process.Pid); child != nil {
			out["child"] = child
		}
		foreground, ancestors := b.foregroundInfo()
		if foreground != nil {
			out["foreground"] = foreground
		}
		if len(ancestors) > 0 {
			out["ancestors"] = ancestors
		}
	}
	return out
}

func (b *block) monitorProcess() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var previous object
	var generation uint64
	for {
		select {
		case <-b.outputDone:
			return
		case <-ticker.C:
			b.mu.Lock()
			if b.closed || b.exit != nil {
				b.mu.Unlock()
				return
			}
			b.inferApplication()
			pending := b.pending
			b.pending = nil
			foreground, ancestors := b.foregroundInfo()
			current := object{"foreground": foreground}
			if len(ancestors) > 0 {
				current["ancestors"] = ancestors
			}
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
			if reflect.DeepEqual(previous, current) {
				continue
			}
			previous = current
			generation++
			payload := cloneObject(current)
			payload["generation"] = generation
			b.s.mu.Lock()
			if v, _ := b.s.findSession(b.sessionID); !b.s.closed && v != nil && v.blocks[b.id] == b {
				b.event("process_changed", payload)
			}
			b.s.mu.Unlock()
		}
	}
}
