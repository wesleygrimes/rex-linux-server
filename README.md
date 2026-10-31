# Tuxosaurus

Your laptop goes to sleep. Your shells don't have to.

Tuxosaurus is a Linux server for [Rex](https://www.superlogical.com/rex), the
terminal app for macOS and iOS. Run it on a Linux machine you own, add that
machine as a host in Rex, and your terminal sessions live on Linux instead of on
your Mac or iPhone.

Tuxosaurus is not affiliated with or endorsed by Superlogical, the makers of Rex.

## Why use it

Rex keeps sessions on a server. Out of the box, that server is your Mac. With
Tuxosaurus, it is a Linux box. Close the laptop, lose signal on your phone, or
switch devices, and the shells keep running. A long build, a dev server or a
coding agent is still there when you reconnect, from Rex on macOS or on iOS.

## How it works

Tuxosaurus is a headless Go program. Each shell runs in a Linux PTY.
libghostty-vt tracks the terminal state and produces the screen snapshots Rex
draws when a client attaches or reconnects.

The server joins Tailscale as its own device, using an embedded Tailscale
client, so the Linux host does not need the Tailscale daemon installed. Rex
connects to it over HTTPS on your tailnet. By default, only devices signed in to
the same Tailscale account that enrolled the server can connect.

Tested with the Rex Beta 0.1.0 (1101) macOS client and the Rex iOS app against
a Linux x86-64 server. See [Compatibility](docs/compatibility.md) for supported
features and limitations.

## Quick start

On the Linux machine, with [mise](https://mise.jdx.dev) installed and activated in
your shell:

```sh
git clone https://github.com/wesleygrimes/tuxosaurus.git
cd tuxosaurus
mise install
bash scripts/build.sh native
install -Dm755 "bin/tuxosaurus-linux-$(go env GOARCH)" "$HOME/.local/bin/tuxosaurus"
~/.local/bin/tuxosaurus setup \
  --state-dir "$HOME/.local/state/tuxosaurus" \
  --tailscale-hostname tuxosaurus
install -Dm644 deploy/tuxosaurus.service "$HOME/.config/systemd/user/tuxosaurus.service"
systemctl --user daemon-reload
systemctl --user enable --now tuxosaurus.service
```

Setup prints a sign-in link and, once you approve it, an HTTPS address. In Rex
on your Mac or iPhone, choose **Add Remote Host** and enter that address. The
sections below explain each step.

## Build

Requires Go 1.26.6 or newer, Zig 0.16.0, a C compiler, curl, and tar.
`mise install` installs the pinned Go and Zig versions from `mise.toml`.

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

The pinned terminal library provides the snapshot format used by Rex clients.
Build with that version to keep snapshots compatible.

## Set up

On the Linux host, install the executable and enroll it in Tailscale:

```sh
install -Dm755 "bin/tuxosaurus-linux-$(go env GOARCH)" "$HOME/.local/bin/tuxosaurus"
~/.local/bin/tuxosaurus setup \
  --state-dir "$HOME/.local/state/tuxosaurus" \
  --tailscale-hostname tuxosaurus
```

Open the authorization link on any device and sign in with the Tailscale account
used by the Mac or iPhone running Rex. Setup waits for authorization and any required device
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
~/.local/bin/tuxosaurus \
  --listen "unix://$HOME/.local/state/tuxosaurus/server.sock" \
  --state-dir "$HOME/.local/state/tuxosaurus" \
  --shell /bin/bash
```

For a systemd user service:

```sh
install -Dm644 deploy/tuxosaurus.service "$HOME/.config/systemd/user/tuxosaurus.service"
systemctl --user daemon-reload
systemctl --user enable --now tuxosaurus.service
```

The service starts with the user's systemd manager and runs as that user. It
uses Bash as a login shell. To start it before login, configure systemd lingering
for that account.

In Rex on your Mac or iPhone, choose **Add Remote Host** and enter the HTTPS
address printed by setup. Create a session on that host to run commands on Linux.

The Unix socket checks OS peer credentials for local administration. The supplied
service also opens an anonymous HTTP listener on `127.0.0.1:51433`. Keep plain HTTP
on loopback; anyone who can reach that listener has shell access.

To inspect or stop the service:

```sh
systemctl --user status tuxosaurus.service
journalctl --user -u tuxosaurus.service
systemctl --user stop tuxosaurus.service
```

Sessions survive client disconnects. They end when the server stops or the machine
reboots. The state directory stores the server identity and Tailscale credentials.

## Development

[Development](docs/development.md) covers the build environment and tests.
[Protocol](docs/protocol.md) describes the control and terminal streams.
[Configuration](docs/configuration.md) covers Lua, host records and block plugins.

## License

MIT. See [LICENSE](LICENSE).
