package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type storedHosts struct {
	Version int      `json:"version"`
	Records []object `json:"records"`
	Removed []string `json:"removed,omitempty"`
}

func validTypedID(value, kind string) bool {
	if !strings.HasPrefix(value, kind+":") {
		return false
	}
	uuid := strings.TrimPrefix(value, kind+":")
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	return err == nil
}

func (s *Server) readHosts() (storedHosts, error) {
	list := storedHosts{Version: 1, Records: []object{}}
	if s.stateDir == "" {
		return list, nil
	}
	path := filepath.Join(s.stateDir, "hosts.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return list, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &list)
		if err == nil && list.Version != 1 {
			err = fmt.Errorf("unsupported hosts version %d", list.Version)
		}
	}
	if err != nil {
		return list, &apiError{"conflict", fmt.Sprintf("cannot read hosts file %s: %v", path, err), 409}
	}
	if list.Records == nil {
		list.Records = []object{}
	}
	return list, nil
}

func (s *Server) writeHosts(list storedHosts) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.stateDir, ".hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(s.stateDir, "hosts.json"))
}

func hostsHash(hosts []object) string {
	data, _ := json.Marshal(hosts)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func validateHost(host object) error {
	if value, present := host["id"]; present && !validTypedID(fmt.Sprint(value), "host") {
		return bad("host.id must be a host record ID")
	}
	label := text(host, "label")
	if label == "" || strings.TrimSpace(label) != label || utf8.RuneCountInString(label) > 64 || strings.ContainsFunc(label, unicode.IsControl) {
		return bad("host.label must contain 1 to 64 characters without surrounding whitespace or control characters")
	}
	endpoints, ok := host["endpoints"].([]any)
	if !ok || len(endpoints) == 0 {
		return bad("host.endpoints needs at least one server address")
	}
	for _, value := range endpoints {
		endpoint, ok := value.(string)
		if !ok || endpoint == "" || strings.TrimSpace(endpoint) != endpoint {
			return bad("host.endpoints contains an invalid server address")
		}
		if strings.HasPrefix(endpoint, "unix:") || strings.HasPrefix(endpoint, "npipe:") || strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, `\\`) {
			return bad("host.endpoints cannot contain a local socket or named pipe")
		}
		if strings.Contains(endpoint, "://") {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Hostname() == "" || parsed.User != nil || !slices.Contains([]string{"http", "https", "ws", "wss"}, parsed.Scheme) {
				return bad("host.endpoints contains an invalid server URL")
			}
		} else if strings.Contains(endpoint, ":") {
			if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
				return bad("host.endpoints contains an invalid host and port")
			}
		} else if strings.ContainsAny(endpoint, " /\\?#@") {
			return bad("host.endpoints contains an invalid hostname")
		}
	}
	return nil
}

func (s *Server) hostRequest(method string, a object) (any, error) {
	list, err := s.readHosts()
	if err != nil {
		return nil, err
	}
	if method != "hosts.list" && s.stateDir == "" {
		return nil, &apiError{"not_supported", "host storage requires a state directory", 501}
	}
	var result object
	changed := false
	switch method {
	case "hosts.put":
		host := maps.Clone(asObject(a["host"]))
		if err := validateHost(host); err != nil {
			return nil, err
		}
		delete(host, "source")
		if _, exists := host["id"]; !exists {
			host["id"] = id("host")
		}
		index := -1
		for _, record := range s.mergedHosts(list) {
			if record["id"] != host["id"] && strings.EqualFold(text(record, "label"), text(host, "label")) {
				return nil, &apiError{"conflict", fmt.Sprintf("host label %q is already used by %s", host["label"], record["id"]), 409}
			}
		}
		for i, record := range list.Records {
			if record["id"] == host["id"] {
				index = i
			}
		}
		delete(host, "updated_at")
		if index >= 0 {
			old := maps.Clone(list.Records[index])
			delete(old, "updated_at")
			if reflect.DeepEqual(old, host) {
				return object{"host": list.Records[index], "hash": hostsHash(s.mergedHosts(list))}, nil
			}
		}
		host["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		if index >= 0 {
			list.Records[index] = host
		} else {
			list.Records = append(list.Records, host)
		}
		changed = true
		list.Removed = slices.DeleteFunc(list.Removed, func(id string) bool { return id == text(host, "id") })
		result = object{"host": host}
	case "hosts.remove":
		hostID := text(a, "id")
		if !validTypedID(hostID, "host") {
			return nil, bad("id must be a host record ID")
		}
		for _, host := range s.mergedHosts(list) {
			if host["id"] == hostID {
				changed = true
				break
			}
		}
		list.Records = slices.DeleteFunc(list.Records, func(host object) bool { return host["id"] == hostID })
		if changed && s.configVM != nil {
			for _, host := range s.configVM.hosts {
				if host["id"] == hostID && !slices.Contains(list.Removed, hostID) {
					list.Removed = append(list.Removed, hostID)
				}
			}
		}
		result = object{"removed": changed}
	default:
		result = object{"hosts": s.mergedHosts(list)}
	}
	slices.SortFunc(list.Records, func(a, b object) int {
		if order := strings.Compare(strings.ToLower(text(a, "label")), strings.ToLower(text(b, "label"))); order != 0 {
			return order
		}
		return strings.Compare(text(a, "id"), text(b, "id"))
	})
	if changed {
		if err := s.writeHosts(list); err != nil {
			return nil, err
		}
	}
	result["hash"] = hostsHash(s.mergedHosts(list))
	if changed {
		s.emitLocked("hosts_changed", object{"hash": result["hash"]}, "")
	}
	return result, nil
}
