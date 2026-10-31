# Rex Linux server

Run Rex terminal sessions on Linux from your Mac.

This is an independent, headless server for the Rex macOS client. Shells run in
Linux PTYs and keep running when the client disconnects. The server joins
Tailscale as its own device and accepts connections over HTTPS.

Tested with the Rex Beta 0.1.0 (1101) macOS client and a Linux x86-64 server. See
[Compatibility](docs/compatibility.md) for supported features and limitations.

## Build

Requires Go 1.26.6 or newer, Zig 0.16.0, a C compiler, curl, and tar.

```sh
# Build on the Linux host.
bash scripts/build.sh native

# Build for Linux x86-64 from an Apple Silicon Mac.
ZIG=/path/to/zig-0.16.0/zig bash scripts/build.sh linux-amd64
```

The script downloads and verifies a pinned Ghostty source archive, builds
libghostty-vt, and links it into the server. The Linux executable still uses the
system C library. The first build can take several minutes; downloads and build
files stay in `.build/`. `JOBS` defaults to 4.

The pinned terminal library provides the snapshot format used by the Mac client.
Build with that version to keep snapshots compatible.

## Set up

On the Linux host, install the executable and enroll it in Tailscale:

```sh
install -Dm755 "bin/rex-linux-server-linux-$(go env GOARCH)" "$HOME/.local/bin/rex-linux-server"
~/.local/bin/rex-linux-server setup \
  --state-dir "$HOME/.local/state/rex-linux-server" \
  --tailscale-hostname rex-linux
```

Open the authorization link on any device and sign in with the Tailscale account
used by the connecting Mac. Setup waits for authorization and any required device
approval, then prints the HTTPS address. Tailscale HTTPS certificates must be
enabled for the tailnet.

Setup saves the server's identity and sign-in. Later starts reuse them. Stop the
server before running setup again.

The Linux host does not need a system Tailscale daemon for this connection. The
Mac needs Tailscale. By default, the server admits only peers belonging to the
account that enrolled it. `--tailscale-allow-tailnet-members` also admits other
identified peers allowed by the tailnet's ACLs.

## Run

For a foreground server:

```sh
~/.local/bin/rex-linux-server \
  --listen "unix://$HOME/.local/state/rex-linux-server/server.sock" \
  --state-dir "$HOME/.local/state/rex-linux-server" \
  --shell /bin/bash
```

For a systemd user service:

```sh
install -Dm644 deploy/rex-linux-server.service "$HOME/.config/systemd/user/rex-linux-server.service"
systemctl --user daemon-reload
systemctl --user enable --now rex-linux-server.service
```

The service starts with the user's systemd manager and runs as that user. It
uses Bash as a login shell. To start it before login, configure systemd lingering
for that account.

In Rex on the Mac, choose **Add Remote Host** and enter the HTTPS address printed
by setup. Create a session on that host to run commands on Linux.

The Unix socket checks OS peer credentials for local administration. The supplied
service also opens an anonymous HTTP listener on `127.0.0.1:51433`. Keep plain HTTP
on loopback; anyone who can reach that listener has shell access.

To inspect or stop the service:

```sh
systemctl --user status rex-linux-server.service
journalctl --user -u rex-linux-server.service
systemctl --user stop rex-linux-server.service
```

Sessions survive client disconnects. They end when the server stops or the machine
reboots. The state directory stores the server identity and Tailscale credentials.

## Development

[Development](docs/development.md) covers the build environment and tests.
[Protocol](docs/protocol.md) describes the control and terminal streams.
[Configuration](docs/configuration.md) covers Lua, host records and block plugins.
