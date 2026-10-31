# Protocol

The server exposes control and terminal streams over WebSockets. The embedded
Tailscale node serves them over HTTPS. Unix sockets support local administration.

## Control stream

Connect to `/api/control` with the `rex.control.v1` subprotocol. The client sends
a JSON hello containing its metadata and, when reconnecting, a reconnect token.

The welcome identifies the protocol, client ID, reconnect token, and message-size
limit. Control messages are limited to 1 MiB.

Requests have this shape:

```json
{"type":"request","id":"1","method":"session.list","payload":{}}
```

Responses contain the request ID, the current event sequence, and either a
`payload` or an `error` with `code`, `message`, and HTTP-style `status`.

Events use `type: "event"` and increment the connection's sequence number.
Session events include the session ID and a nested event. Responses and events
are queued in order on the same connection.

`session.bootstrap` initializes subscriptions. Session attachment determines
which session events reach the client. `GET /api/help` lists method schemas and installed plugin APIs. Plain-text and
HTML guides are available at `/llms.txt` and `/llms.html`.

## Terminal stream

Request a ticket with `data.connect`, supplying `session_id`, `block_id`,
`data_ref_id: "terminal"`, and `subprotocol: "rex.data.terminal.v1"`.
A ticket is a single-use, base64url-encoded 16-byte value that expires after
60 seconds.

Connect to `/api/data` with the `rex.data.terminal.v1` subprotocol and send a
binary handshake. Integer fields use little-endian encoding.

| Message | Bytes |
| --- | --- |
| Hello | `01`, then the 16-byte ticket. |
| Resume | `02`, a 16-byte reconnect token, then the received byte count as uint64. |
| Welcome | `81`, a 16-byte reconnect token, three reserved zero bytes, reconnect grace in milliseconds as uint32, and a nonce as uint64. |
| Reject | `82`, then a one-byte reason. |

The welcome is 32 bytes. A fresh stream's payload starts with a Ghostty snapshot
whose signature is `GHOSTSNP`, followed by terminal updates. WebSocket frame
boundaries do not delimit the snapshot.

Resize, theme and child-exit updates use APC markers carrying the stream nonce. A new stream
includes the current geometry, theme and retained exit state alongside its snapshot.

Terminal input goes through the control API. The data stream carries output.
On resume, the client supplies the number of payload bytes it received, including
the snapshot and markers. The server replays the missing bytes if they remain
within the 8 MiB buffer and 60-second reconnect window. Otherwise, the client
needs a fresh ticket and snapshot.

## Connection identity

Data tickets and reconnect tokens are bound to the authenticated principal.
Tailscale connections use peer identity; owner-only admission rejects tagged
nodes and peers belonging to another account. Unix connections use OS peer
credentials. Plain HTTP listeners are anonymous.

Tailscale enable, disable, login, logout, and rename methods require local Unix
administration. An authenticated remote connection cannot invoke them.
