package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	ghostty "go.mitchellh.com/libghostty"
)

type blockEvent struct {
	name    string
	payload object
}

type clipboardWrite struct {
	id      uint64
	text    string
	created time.Time
}

// Retain up to four writes for one minute, within a total of 16 MiB.
// Clipboard bytes stay in memory to avoid plaintext files on disk.
func (b *block) pruneClipboard(now time.Time) {
	first, total := len(b.clipboard), 0
	for i := len(b.clipboard) - 1; i >= 0; i-- {
		write := b.clipboard[i]
		if now.Sub(write.created) >= time.Minute || len(b.clipboard)-i > 4 || total+len(write.text) > 16<<20 {
			break
		}
		total += len(write.text)
		first = i
	}
	if first > 0 {
		b.clipboard = append([]clipboardWrite(nil), b.clipboard[first:]...)
	}
}

// Called synchronously by VTWrite with b.mu held. Queue the event for emission
// after releasing the terminal lock to preserve the server's lock ordering.
func (b *block) storeClipboard(_ *ghostty.Terminal, request ghostty.ClipboardWrite) ghostty.ClipboardWriteReply {
	for _, content := range request.Contents {
		mime := strings.ToLower(content.MIME)
		if mime != "text/plain" && !strings.HasPrefix(mime, "text/plain;") {
			continue
		}
		if len(content.Data) > 16<<20 {
			return ghostty.ClipboardWriteReply{Result: ghostty.ClipboardWriteIOError}
		}
		now := time.Now()
		b.clipboardID++
		write := clipboardWrite{id: b.clipboardID, text: string(content.Data), created: now}
		b.clipboard = append(b.clipboard, write)
		b.pruneClipboard(now)
		event := object{"id": write.id}
		if len(write.text) > 0 {
			event["bytes"] = len(write.text)
			if len(write.text) <= 0xc000 {
				event["text"] = write.text
				encoded, _ := json.Marshal(event)
				if len(encoded) > 0xc000 {
					delete(event, "text")
				}
			}
		}
		b.pending = append(b.pending, blockEvent{"clipboard_written", event})
		return ghostty.ClipboardWriteReply{Result: ghostty.ClipboardWriteSuccess}
	}
	return ghostty.ClipboardWriteReply{Result: ghostty.ClipboardWriteUnsupported}
}

func (b *block) readClipboard(a object, now time.Time) (any, error) {
	offset, budget := number(a, "offset", 0), number(a, "max_bytes", 0)
	if offset < 0 {
		return nil, bad(fmt.Sprintf("input: validating: offset %d is negative", offset))
	}
	if budget < 0 {
		return nil, bad(fmt.Sprintf("input: validating: max_bytes %d is negative", budget))
	}
	id := uint64(number(a, "id", 0))
	b.pruneClipboard(now)
	var write *clipboardWrite
	for i := len(b.clipboard) - 1; i >= 0; i-- {
		if id == 0 || b.clipboard[i].id == id {
			write = &b.clipboard[i]
			break
		}
	}
	if write == nil {
		if id == 0 {
			return nil, missing("not found: no clipboard write is stored")
		}
		return nil, missing(fmt.Sprintf("not found: clipboard write %d", id))
	}
	if budget == 0 {
		budget = 0xc000
	}
	budget = min(budget, 0xf0000)
	offset = min(offset, len(write.text))
	end := offset + min(max(4, budget), len(write.text)-offset)
	chunk := write.text[offset:end]
	length := jsonPrefixLength(chunk, budget)
	return object{"id": write.id, "data": chunk[:length], "total": len(write.text), "eof": offset+length == len(write.text)}, nil
}

func jsonPrefixLength(text string, budget int) int {
	used, n := 0, 0
	for n < len(text) && utf8.FullRuneInString(text[n:]) {
		r, width := utf8.DecodeRuneInString(text[n:])
		cost := width
		switch r {
		case '"', '\\', '\n', '\r', '\b', '\f', '\t':
			cost = 2
		case '<', '>', '&', '\u2028', '\u2029':
			cost = 6
		default:
			if r < 0x20 {
				cost = 6
			}
		}
		if used+cost > budget {
			if n == 0 {
				return width
			}
			break
		}
		used += cost
		n += width
	}
	return n
}
