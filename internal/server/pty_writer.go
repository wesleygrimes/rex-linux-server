package server

import (
	"io"
	"os"
	"sync"
)

// PTY input must not wait for the child while holding the terminal/server lock:
// the child can be waiting for output to drain through those same locks.
type ptyWriter struct {
	mu     sync.Mutex
	queue  [][]byte
	wake   chan struct{}
	closed bool
	err    error
}

func newPTYWriter(file io.Writer) *ptyWriter {
	w := &ptyWriter{wake: make(chan struct{}, 1)}
	go w.run(file)
	return w
}

func (w *ptyWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, os.ErrClosed
	}
	if len(data) > 0 {
		w.queue = append(w.queue, append([]byte(nil), data...))
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

func (w *ptyWriter) run(file io.Writer) {
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		if len(w.queue) == 0 {
			w.mu.Unlock()
			<-w.wake
			continue
		}
		data := w.queue[0]
		w.queue[0] = nil
		w.queue = w.queue[1:]
		if len(w.queue) == 0 {
			w.queue = nil
		}
		w.mu.Unlock()
		for len(data) > 0 {
			n, err := file.Write(data)
			data = data[n:]
			if err == nil && n == 0 {
				err = io.ErrShortWrite
			}
			if err != nil {
				w.mu.Lock()
				w.err, w.closed, w.queue = err, true, nil
				w.mu.Unlock()
				return
			}
		}
	}
}

func (w *ptyWriter) close() {
	w.mu.Lock()
	w.closed, w.queue = true, nil
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
