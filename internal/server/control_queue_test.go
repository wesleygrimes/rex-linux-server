package server

import (
	"context"
	"strings"
	"testing"
)

func TestControlQueueOwnsFramesAndBoundsBytes(t *testing.T) {
	q := newControlQueue()
	frame := object{"label": "before"}
	if !q.push(frame) {
		t.Fatal("cannot enqueue frame")
	}
	frame["label"] = "after"
	data, ok := q.next(context.Background())
	if !ok || string(data) != `{"label":"before"}` {
		t.Fatalf("queued frame changed: %s", data)
	}
	payload := strings.Repeat("x", (1<<20)-2)
	for i := 0; i < 8; i++ {
		if !q.push(payload) {
			t.Fatalf("queue overflowed at %d MiB", i)
		}
	}
	if q.push("") {
		t.Fatal("queue exceeded byte budget")
	}
	q.next(context.Background())
	if !q.push(payload) {
		t.Fatal("consumed bytes were not released")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := q.next(ctx); ok {
		t.Fatal("cancelled writer consumed a frame")
	}
}
