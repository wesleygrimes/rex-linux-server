package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostStateSurvivesRestartAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "init.lua")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	s, err := New(state, "/bin/sh", config)
	if err != nil {
		t.Fatal(err)
	}
	hostID := s.hostID
	value, err := s.hostRequest("hosts.put", object{"host": object{"label": "saved host", "endpoints": []any{"saved.example.ts.net"}, "icon": "globe"}})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	record := asObject(asObject(value)["host"])
	s.Close()
	s, err = New(state, "/bin/sh", config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	value, err = s.hostRequest("hosts.list", object{})
	if err != nil {
		t.Fatal(err)
	}
	hosts := asObject(value)["hosts"].([]object)
	if len(hosts) != 1 || asObject(hosts[0])["id"] != record["id"] || asObject(hosts[0])["icon"] != "globe" || s.hostID != hostID {
		t.Fatalf("saved identity/host changed: %v", value)
	}
	path := filepath.Join(state, "hosts.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("host state permissions: %v", info.Mode())
	}
	corrupt := []byte("{broken host state")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"hosts.list", "hosts.put", "hosts.remove"} {
		_, err := s.hostRequest(method, object{"id": record["id"], "host": record})
		if failure, ok := err.(*apiError); !ok || failure.Code != "conflict" {
			t.Fatalf("%s ignored corrupt state: %v", method, err)
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(corrupt) {
		t.Fatalf("corrupt state was overwritten: %q %v", data, err)
	}
}
