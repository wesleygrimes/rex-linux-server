package server

import "time"

// Server-inserted escape sequences must fall between complete child sequences.
// If a child leaves the parser unfinished, abandon its parser state after the
// grace period and require data clients to take a new snapshot.
func (b *block) scheduleMutation(kind string, apply func() error) error {
	if b.closed {
		return missing("block is closed")
	}
	ground, err := b.term.VTGround()
	if err != nil {
		return err
	}
	if ground && len(b.mutations) == 0 {
		return apply()
	}
	for i, mutation := range b.mutations {
		if mutation.kind == kind {
			b.mutations[i].apply = apply
			return nil
		}
	}
	b.mutations = append(b.mutations, terminalMutation{kind, apply})
	if b.mutationTimer == nil {
		b.mutationTimer = time.AfterFunc(250*time.Millisecond, func() {
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				return
			}
			ground, _ := b.term.VTGround()
			if !ground {
				b.term.VTWrite([]byte{0x18})
				for channel := range b.channels {
					channel.close()
				}
			}
			b.applyMutations()
			pending := b.pending
			b.pending = nil
			b.mu.Unlock()
			b.s.mu.Lock()
			if !b.s.closed {
				for _, event := range pending {
					b.event(event.name, event.payload)
				}
			}
			b.s.mu.Unlock()
		})
	}
	return nil
}

type terminalMutation struct {
	kind  string
	apply func() error
}

func (b *block) applyMutations() {
	if b.mutationTimer != nil {
		b.mutationTimer.Stop()
		b.mutationTimer = nil
	}
	mutations := b.mutations
	b.mutations = nil
	for _, mutation := range mutations {
		mutation.apply()
	}
}

func (b *block) writeOutput(data []byte) {
	if len(b.mutations) > 0 {
		n, _ := b.term.VTWriteUntilGround(data)
		for channel := range b.channels {
			channel.append(data[:n])
		}
		data = data[n:]
		if ground, _ := b.term.VTGround(); ground {
			b.applyMutations()
		}
	}
	if len(data) > 0 {
		b.term.VTWrite(data)
		for channel := range b.channels {
			channel.append(data)
		}
	}
}
