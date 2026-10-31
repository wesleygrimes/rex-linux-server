package server

import (
	"context"
	"encoding/json"
	"sync"
)

// Encode before queuing so queued frames own their contents and the limit
// measures wire bytes rather than a varying number of events.
type controlQueue struct {
	mu     sync.Mutex
	frames [][]byte
	bytes  int
	wake   chan struct{}
}

func newControlQueue() *controlQueue {
	return &controlQueue{wake: make(chan struct{}, 1)}
}

func (q *controlQueue) push(value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.bytes+len(data) > 8<<20 {
		return false
	}
	q.frames = append(q.frames, data)
	q.bytes += len(data)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *controlQueue) next(ctx context.Context) ([]byte, bool) {
	for ctx.Err() == nil {
		q.mu.Lock()
		if len(q.frames) > 0 {
			data := q.frames[0]
			q.frames[0] = nil
			q.frames = q.frames[1:]
			q.bytes -= len(data)
			q.mu.Unlock()
			return data, true
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
		}
	}
	return nil, false
}
