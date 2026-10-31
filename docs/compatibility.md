# Compatibility

The compatibility target is Rex Beta 0.1.0 (1101), with server revision
`a601bcd3cd2851cd469c6a9c6be081ceb0e76e4f`. Protocol tests run the same scenarios
against that server and this implementation. New Rex releases may change the
protocol.

| Area | Support |
| --- | --- |
| Sessions and windows | Bootstrap, attachment, labels, focus, creation and cleanup. |
| Layouts | Tiled splits, floating layers, detached blocks, moves, swaps, resizing, zoom and navigation. |
| Terminal output | Ghostty snapshots, VT output, exit markers and reconnect replay. |
| Input | Keyboard, paste, focus reporting, application mouse events, drag selection, scrollback wheel input and OSC 8 link clicks. |
| Themes | Default colors, palette updates and fresh-stream restoration. |
| Directory browsing | Child-relative paths, prefix filtering, optional files and symlink handling. |
| Clipboard | OSC 52 transfers, inline events and chunked reads. |
| Process and status | Foreground jobs, owner attribution, explicit OSC 7501 records, OSC progress and application activity rules. |
| Client actions | Action discovery, argument validation, queued calls, replies, timeouts and disconnect handling. |
| Lua and shortcuts | Configuration actions, client scripts, key sequences, shortcut modes, overrides and configuration reload. |
| Hosts | Saved and Lua-configured hosts, overrides, removal tombstones and change events. |
| Block plugins | Executable discovery, methods, actions, events and reload with existing block IDs. |
| Tailscale | Embedded node identity, HTTPS, account-based admission and saved sign-in. |
| Administration | Unix peer credentials and local-only Tailscale administration. |

Executable block plugins have no terminal data provider in this protocol version.
They expose control methods and events.

## Limits and lifecycle

The default limits are 64 sessions, 64 windows per session, 64 placed blocks per
window and 1,024 blocks across the server. Override them with `--max-sessions`,
`--max-windows`, `--max-panes` and `--max-blocks`. Zero disables a limit.

Terminal streams retain up to 8 MiB for replay and allow reconnects for 60 seconds.
Control clients also retain their identity and attachments during that window.
`--exit-empty` waits for retained control clients to expire before stopping.

Each terminal keeps up to four clipboard writes for one minute, within 16 MiB,
and up to 64 explicit program status records.

Clear, reset, resize and stream markers wait for a complete terminal escape
sequence. After 250 ms, an unfinished sequence forces parser recovery and closes
existing data streams. Clients then need a fresh ticket and snapshot. A fresh
snapshot requested while the parser is unfinished is rejected.

Sessions end when the server stops. Saved state preserves host records and
Tailscale identity, not running processes or terminal contents.

## Verification

The shared suites cover the control method catalog, Lua and shortcut behavior,
host records, client reconnects and action queues, target resolution, layouts,
terminal ownership, process tracking, input, clipboard transfers and snapshots.
Plugin checks use an executable fixture for methods, actions, events, launcher
processes and reload. Resource tests check rejected layouts for leaked blocks.

Application activity tests use a named fixture with title and screen signals.
An application's display can change between versions. These tests do not cover
every rule for every application.

The Linux integration suite starts temporary servers and real PTYs. Package tests
and the macOS integration suite also run with the race detector. Optional Rex CLI
checks decode snapshots independently. The macOS client has connected over
Tailscale, run Linux commands and opened splits. The Rex iOS app also connects
and runs sessions (smoke tested 10/31/2026). The expanded features still need a
client smoke test after deployment.

See [Development](development.md) for commands. Skipped interoperability checks
are not passing checks.
