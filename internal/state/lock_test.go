package state

import "testing"

func TestExclusiveState(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(dir); err == nil {
		second()
		t.Fatal("two processes could share the same state")
	}
	unlock()
	second, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	second()
}
