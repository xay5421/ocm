# ocm

<img src="assets/ocm.svg" width="64" align="right" alt="ocm logo">

`ocm` (opencode connection manager) manages [opencode](https://opencode.ai)
servers across machines: it starts `opencode serve` on remote hosts over SSH,
keeps SSH tunnels to them alive, auto-discovers local instances, and attaches
your local TUI to any of them — so you can keep long-running agent sessions on
remote machines and reconnect from anywhere.

## Features

- **One-command connect**: `ocm connect <host>` brings up the SSH tunnel,
  starts `opencode serve` on the remote machine if needed, waits for it to be
  healthy, and attaches your local opencode TUI.
- **SSH tunnels, managed**: tunnels are started with keep-alive options,
  reused when already running, and torn down with `ocm down`.
- **Local auto-discovery**: locally running opencode servers (including ones
  embedded in editors) are discovered automatically — no config needed.
- **Status at a glance**: `ocm list` / `ocm status` show tunnel state, server
  health, version, and active sessions per host (`--json` for scripting).
- **Dashboard app**: `ocm dashboard` opens a native window (Windows/macOS)
  with per-host health and up/down/restart actions.
- **Password support**: servers can be protected with HTTP basic auth
  (opencode's `OPENCODE_SERVER_PASSWORD`); ocm exports the password when
  starting servers and authenticates with it, keeping it off command lines.
- **Cross-platform**: works on Windows, macOS, and Linux.

## Requirements

- [opencode](https://opencode.ai) installed on the local machine and on each
  remote host
- `ssh` available locally, with the remote hosts reachable non-interactively
  (key-based auth; an alias in `~/.ssh/config` is recommended)
- `curl` on the remote hosts (used for the health pre-check)
- Go 1.24+ (only to build from source)

## Install

```sh
go install github.com/xay5421/ocm@latest
```

Or build from source:

```sh
git clone https://github.com/xay5421/ocm.git
cd ocm
go build .
```

## Quick start

1. Make sure the remote machine is reachable via ssh, e.g. with an entry in
   `~/.ssh/config`:

   ```
   Host mybox
       HostName mybox.example.com
       User me
   ```

2. Add the host to `~/.config/ocm/config.json` (created with an example entry
   on first run):

   ```json
   {
     "hosts": {
       "mybox": {
         "ssh": "mybox",
         "remote_port": 4096,
         "local_port": 14001,
         "opencode": "~/.opencode/bin/opencode"
       }
     }
   }
   ```

3. Connect:

   ```sh
   ocm connect mybox
   ```

   This starts the tunnel, launches `opencode serve` on `mybox` if it is not
   already running, and attaches your local TUI. Detach whenever you like —
   sessions keep running on the remote machine, and `ocm connect mybox` picks
   them back up.

## Usage

```
ocm list [--json]                 List hosts and their status
ocm status [--json]               List hosts with sessions and live status
ocm up <host>                     Ensure tunnel + remote opencode serve
ocm down <host> [--serve]         Stop tunnel (--serve also stops remote server)
ocm connect <host> [dir] [args…]  Up + attach local TUI to the remote server
ocm run <host> [args…] <prompt>   Up + run a prompt on the remote server
ocm restart <host>                Restart the remote server (e.g. after config change)
ocm up local                      Start a local opencode serve (fixed port 14000)
ocm down local [pid]              Stop a discovered local server
ocm restart local [pid]           Restart a local server (fixed port 14000)
ocm dashboard [--port N] [--up]   Open the dashboard app window (Windows/macOS)
ocm config                        Print config file path and contents
ocm version                       Print the ocm version
```

Notes:

- Host names support fuzzy matching: `ocm connect my` works if it uniquely
  matches `mybox`.
- Extra arguments after `<host>` are passed through to `opencode attach` /
  `opencode run`.
- `local` is a special host name for servers on this machine; they are
  auto-discovered, and dedicated `opencode serve` processes are preferred over
  editor-embedded ones.
- `ocm down <host>` only closes the tunnel; the remote server (and its
  sessions) keep running. Add `--serve` to stop the remote server too.
- Remote server logs go to `~/.opencode-serve.log` on the remote machine.

## Configuration

The config lives at `~/.config/ocm/config.json` (override with the
`$OCM_CONFIG` environment variable). Fields per host:

| Field         | Required | Description                                                        |
| ------------- | -------- | ------------------------------------------------------------------ |
| `ssh`         | yes      | SSH destination: alias from `~/.ssh/config` or `user@host`          |
| `remote_port` | yes      | Port `opencode serve` listens on, on the remote machine             |
| `local_port`  | yes      | Local port the SSH tunnel binds to (must be unique per host)        |
| `opencode`    | yes      | Path of the opencode binary on the remote machine                   |
| `dir`         | no       | Default remote working directory for `ocm connect`                  |
| `password`    | no       | Password for that remote server (overrides the global default)      |

Top-level fields:

| Field            | Description                                                       |
| ---------------- | ----------------------------------------------------------------- |
| `hosts`          | Map of host name → host entry                                      |
| `password`       | Global default password for all servers (remote and local)         |
| `local_password` | Password for local servers started by ocm (overrides the default)   |

### Passwords

A top-level `password` acts as the default for every server; a host's own
`password` field and `local_password` take precedence where set.

If a password applies to a server, ocm exports it as
`OPENCODE_SERVER_PASSWORD` when starting the server, and uses it to
authenticate health checks, session listings, and the attached TUI. The
password is passed to the remote process via stdin so it never appears on the
remote command line. Since the config file may contain passwords, ocm keeps it
at file mode `0600`.

## Dashboard

```sh
ocm dashboard              # open the dashboard app window
ocm dashboard --up         # also bring all configured hosts up first
ocm dashboard --port 4900  # custom port (default 4800)
```

The dashboard is a native app window (WebView2 on Windows, WKWebView on
macOS) showing every host — including auto-discovered local instances — with
health, version, and up/down/restart buttons. Closing the window exits the
dashboard; tunnels and servers keep running. The backing HTTP server binds
to `127.0.0.1` only, and links open in your default browser.

Per platform:

- **Windows**: double-click `ocm-dashboard.exe` (no console window), or run
  `ocm dashboard` in a terminal. Double-clicking `ocm.exe` works too, with a
  brief console flash.
- **macOS**: install `ocm.app` (from the `_app.zip` release asset) into
  Applications and launch it. The CLI binary lives at
  `ocm.app/Contents/MacOS/ocm` if you want to symlink it onto your PATH.
- **Linux**: no native window; `ocm dashboard` serves the UI and prints the
  URL to open manually.

Running `ocm` without arguments in a terminal prints the help text.

## How it works

`ocm up <host>`:

1. Starts `ssh -f -N -L <local_port>:127.0.0.1:<remote_port> <ssh>` if no
   matching tunnel process is running.
2. Probes `http://127.0.0.1:<local_port>/global/health` through the tunnel.
3. If the server is not up, runs `nohup opencode serve …` on the remote host
   over SSH (skipped if something already listens on the port), then waits
   until it reports healthy.

`ocm connect` / `ocm run` do the above, then hand control to your local
`opencode attach <url>` / `opencode run --attach <url>` (replacing the ocm
process on Unix).

## License

[MIT](LICENSE)
