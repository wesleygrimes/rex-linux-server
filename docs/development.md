# Development

## Build environment

Install Go 1.26.6 or newer, Zig 0.16.0, a C compiler, curl, and tar. The protocol
tests also need Python 3 at `/usr/bin/python3`. `mise install` installs the
pinned Go and Zig versions from `mise.toml`.

Build the native executable:

```sh
bash scripts/build.sh native
```

For cross-compilation, pass a target such as `linux-amd64` or `linux-arm64`.
Set `ZIG` if Zig is outside `PATH`. `REX_BUILD_DIR` selects the build directory,
and `ZIG_GLOBAL_CACHE_DIR` can reuse an existing Zig package cache.

Set up the Go bindings after the native build:

```sh
export GHOSTTY_PREFIX="$PWD/.build/ghostty-$(go env GOOS)-$(go env GOARCH)"
export PKG_CONFIG="$PWD/.build/pkg-config-ghostty"
```

These paths assume the default build directory. If `REX_BUILD_DIR` was set, use
that directory instead.

## Tests

Run package tests with the race detector, then vet:

```sh
go test -race ./...
go vet ./...
```

The protocol suite starts a server with temporary state, creates real PTYs, and
checks input, layouts, clipboard transfers, status events, snapshots, and replay:

```sh
go test ./internal/protocol -run '^TestCompatibility$' -count=1 -args \
  -server-binary "$PWD/bin/tuxosaurus-$(go env GOOS)-$(go env GOARCH)"
```

The plugin suite builds its own executable fixture. Resource and startup checks
also launch isolated servers:

```sh
go test ./internal/protocol -run '^TestBlockPlugins$|^TestResourceLimits$|^TestRunOptions$' -count=1 -args \
  -server-binary "$PWD/bin/tuxosaurus-$(go env GOOS)-$(go env GOARCH)"
```

`TestRunOptions` waits through the 60-second control reconnect window. Add
`-rex-binary` to run these scenarios against Rex as well.

A plain `go test` skips this suite unless `-server-binary` or `-endpoint` is
supplied. Use `-endpoint https://your-server.example.ts.net` to test a deployed
instance. The suite creates and removes its own sessions.

Layout fixtures contain fixed expected frames. Tests normalize IDs and connection
metadata before comparing them. Update a fixture only for a documented protocol
change.

## Rex interoperability

On a Mac with Rex installed, add its executable to the protocol test as an
independent snapshot decoder:

```sh
go test ./internal/protocol -run '^TestCompatibility$' -count=1 -args \
  -server-binary "$PWD/bin/tuxosaurus-$(go env GOOS)-$(go env GOARCH)" \
  -rex-binary '/Applications/Rex Beta.app/Contents/Helpers/Rex Server.app/Contents/MacOS/rex'
```

To run the shared protocol checks against Rex itself:

```sh
go test ./internal/protocol -run '^TestRexInterop$' -count=1 -args \
  -rex-binary '/Applications/Rex Beta.app/Contents/Helpers/Rex Server.app/Contents/MacOS/rex'
```

This starts an isolated Rex process with temporary configuration and state.
It does not use existing Rex sessions.

## Writing

Run the `unslop` skill on documentation and template changes before committing or
publishing. Keep instructions concrete, describe limitations plainly, and report
only checks that ran. The same review applies to release notes and issue or PR
templates.
