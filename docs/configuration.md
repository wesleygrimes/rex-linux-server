# Configuration

The server reads `~/.config/rex/init.lua` on Linux. `REX_CONFIG` or `--config PATH`
selects another file. Configuration can declare remote hosts, actions and
shortcuts. It has Lua's standard libraries, including file and process access,
and runs with the server account's permissions.

For example:

```lua
rex.host{
  label = 'Build host',
  endpoints = {'build-host.example.ts.net'},
}

rex.action{
  name = 'new_shell',
  title = 'New shell',
  run = function(ctx)
    return rex.call('session.new_window', {
      session_id = ctx.session_id,
      layout = rex.layout.block{
        flavor = 'com.superlogical.terminal.shell',
        options = {},
      },
    })
  end,
}

rex.bind('ctrl+shift+n', 'new_shell')
```

Load changes through `server.config_reload` on the local Unix control socket.
A load error leaves the previous configuration active. Modules loaded with
`require` are read again on reload.

Host records saved through the API override matching configuration records.
Removing a configured host keeps it hidden on later reloads. Records and removal
tombstones are stored in `hosts.json` under the server state directory.

Client scripts installed through `client.set_script` belong to that client.
They have a restricted Lua environment and can declare key handlers. A failed
replacement leaves the previous script active. Key handlers consume a key only
when they return `true`.

## Block plugins

Place executable block plugins under `$XDG_DATA_HOME/rex/blocks`, which defaults
to `~/.local/share/rex/blocks`. System data directories are also searched. Use
`--block-dir DIR` to select explicit directories; the flag can be repeated.

Plugins receive `REX_BLOCKSERVER_INFO_FD` for reporting their connection details.
They expose creators, methods and events through `rex.control.v1`. Installed
creators appear in `block_creator.list` and the API help catalog.

`--block-plugin-reload` watches executable changes. Compatible replacements keep
existing block IDs and layouts. Changes to a creator's flavor or API metadata
are rejected, leaving the previous process active.
