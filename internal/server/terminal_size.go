package server

import (
	"sort"
	"time"

	"github.com/creack/pty"
)

type desiredSize struct {
	columns, rows, cellWidth, cellHeight int
	updatedAt                            time.Time
	order                                uint64
}

func (b *block) desiredSizes() []any {
	ids := make([]string, 0, len(b.desired))
	for id := range b.desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		size := b.desired[id]
		out = append(out, object{"client_id": id, "columns": size.columns, "rows": size.rows, "updated_at": size.updatedAt})
	}
	return out
}

func (b *block) applySize(size desiredSize) error {
	if err := pty.Setsize(b.file, &pty.Winsize{Cols: uint16(size.columns), Rows: uint16(size.rows), X: uint16(size.columns * size.cellWidth), Y: uint16(size.rows * size.cellHeight)}); err != nil {
		return err
	}
	return b.scheduleMutation("resize", func() error {
		if err := b.term.Resize(uint16(size.columns), uint16(size.rows), uint32(size.cellWidth), uint32(size.cellHeight)); err != nil {
			return err
		}
		b.cols, b.rows, b.cellWidth, b.cellHeight = size.columns, size.rows, size.cellWidth, size.cellHeight
		return nil
	})
}

func (b *block) sizeChanged() {
	b.scheduleMutation("geometry", func() error {
		for channel := range b.channels {
			channel.append(b.resizeMarker(channel.nonce))
		}
		b.pending = append(b.pending, blockEvent{"size_changed", b.size()})
		return nil
	})
}

func (b *block) releaseSize(clientID string) error {
	_, existed := b.desired[clientID]
	delete(b.desired, clientID)
	if b.owner == clientID {
		b.owner = ""
		latest := desiredSize{}
		for id, size := range b.desired {
			if size.order > latest.order {
				b.owner, latest = id, size
			}
		}
		if b.owner != "" {
			if err := b.applySize(latest); err != nil {
				return err
			}
		}
		b.sizeChanged()
	} else if existed {
		b.event("size_changed", b.size())
	}
	return nil
}

func (b *block) resizeForClient(clientID string, a object) (any, error) {
	mode := text(a, "mode")
	if mode != "" && mode != "take" && mode != "advisory" && mode != "release" {
		return nil, bad("resize mode must be take, advisory or release")
	}
	if mode == "release" {
		return object{}, b.releaseSize(clientID)
	}
	size := desiredSize{columns: number(a, "columns", b.cols), rows: number(a, "rows", b.rows), cellWidth: number(a, "cell_width_px", b.cellWidth), cellHeight: number(a, "cell_height_px", b.cellHeight), updatedAt: time.Now().UTC(), order: b.resizeOrder + 1}
	if size.columns < 1 || size.columns > 1000 || size.rows < 1 || size.rows > 1000 {
		return nil, bad("terminal dimensions must be between 1 and 1000")
	}
	if size.cellWidth < 0 || size.cellWidth > 1000 || size.cellHeight < 0 || size.cellHeight > 1000 {
		return nil, bad("invalid cell dimensions")
	}
	if mode != "advisory" || b.owner == "" || b.owner == clientID {
		if err := b.applySize(size); err != nil {
			return nil, err
		}
		b.owner = clientID
	}
	if b.desired == nil {
		b.desired = map[string]desiredSize{}
	}
	if clientID != "" && mode == "advisory" {
		b.desired[clientID] = size
		b.resizeOrder = size.order
	}
	b.sizeChanged()
	return object{}, nil
}

func (s *Server) releaseClientSizes(c *client, sessionID string) {
	for _, session := range s.sessions {
		if sessionID != "" && session.id != sessionID {
			continue
		}
		for _, block := range session.blocks {
			block.mu.Lock()
			if !block.closed {
				block.releaseSize(c.id)
				block.flushEvents()
			}
			block.mu.Unlock()
		}
	}
}
