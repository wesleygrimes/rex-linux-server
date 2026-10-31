package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

func pluginSignature(path string) ([32]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func (s *Server) watchPlugin(path string, current *blockPlugin) {
	defer s.pluginWorkers.Done()
	signature, err := pluginSignature(path)
	if err != nil {
		return
	}
	info, _ := os.Stat(path)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.lifetime.Done():
			return
		case <-ticker.C:
			nextInfo, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info != nil && info.ModTime() == nextInfo.ModTime() && info.Size() == nextInfo.Size() {
				continue
			}
			info = nextInfo
			nextSignature, err := pluginSignature(path)
			if err != nil || nextSignature == signature {
				continue
			}
			next, creators, err := s.startPlugin(path)
			if err != nil {
				log.Printf("reloading block plugin %s: %v", path, err)
				continue
			}
			if err := s.replacePlugin(current, next, creators); err != nil {
				next.close()
				log.Printf("reloading block plugin %s: %v", path, err)
				continue
			}
			current.close()
			current, signature = next, nextSignature
		}
	}
}

func (s *Server) replacePlugin(old, next *blockPlugin, metadata []object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("server stopped")
	}
	byName := map[string]object{}
	for _, creator := range metadata {
		byName[text(creator, "name")] = creator
	}
	for name, creator := range s.creators {
		if creator.process != old {
			continue
		}
		updated := byName[name]
		if updated == nil {
			return fmt.Errorf("plugin no longer provides creator %q", name)
		}
		for _, field := range []string{"flavors", "api"} {
			before, _ := json.Marshal(creator.info[field])
			after, _ := json.Marshal(updated[field])
			if string(before) != string(after) {
				return fmt.Errorf("plugin changed the %s of creator %q", field, name)
			}
		}
	}
	for name, updated := range byName {
		if text(updated, "host_id") == "" {
			updated["host_id"] = s.hostID
		}
		if creator := s.creators[name]; creator != nil && creator.process == old {
			creator.info, creator.process = updated, next
		} else if creator == nil {
			s.creators[name] = &pluginCreator{info: updated, process: next}
		}
	}
	for i, plugin := range s.plugins {
		if plugin == old {
			s.plugins[i] = next
			break
		}
	}
	return nil
}

// A reloaded plugin starts with no live blocks. Recreate on first use while
// keeping the session's block ID and layout intact.
func (b *block) ensureRemote() error {
	r := b.remote
	p := r.creator.process
	if r.bound == p {
		return nil
	}
	input := object{"block_id": b.id, "session_id": b.sessionID, "flavor": r.input["flavor"]}
	if r.input["options"] != nil {
		input["options"] = r.input["options"]
	}
	value, err := p.call(p.ctx, "block.create", input)
	if err != nil {
		return err
	}
	remoteID := text(asObject(value), "block_id")
	if !validTypedID(remoteID, "block") {
		return fmt.Errorf("plugin returned an invalid block ID")
	}
	r.id, r.bound = remoteID, p
	p.mu.Lock()
	p.blocks[remoteID] = b
	p.mu.Unlock()
	_, err = p.call(p.ctx, "block.event", object{"block_id": remoteID, "event": object{"type": "session_attached", "payload": object{"session_id": b.sessionID, "block_id": b.id}}})
	return err
}
