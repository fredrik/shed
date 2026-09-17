# Control plane

The control plane is the command surface behind `ssh shed <command>`
and `bin/shed <command>`. It is one cobra command tree, built per
session, executed against the VM manager, with the ssh session's
streams as stdin, stdout and stderr. Package: `internal/control`.

## How a command reaches cobra

An ssh session whose username is `shed` is handed to `control.Run`
together with a `control.Deps` bundle (manager, config, HTTP gate,
authorized_keys path, local username). `Run`:

1. Splits the raw exec line with `kballard/go-shellquote`, so quoting
   works the way a shell user expects (`ssh shed new --image
   'ghcr.io/x/y:1.0'`). A line that fails to parse prints `shed: parse
   command: ...` and exits 1.
2. Substitutes `help` when there is no exec line at all (`ssh shed`).
3. Builds a fresh cobra root, sets args and the three streams, and
   executes. Any error maps to exit code 1; success to 0.

The root command is named `shed`, has usage silenced (errors print a
one-liner, not the full help), and has cobra's default `completion`
command disabled. Because the tree is rebuilt per session, flag state
never leaks between commands.

`control.Session` is the four-method slice of `gliderlabs/ssh.Session`
the package needs (`RawCommand`, `Read`, `Write`, `Stderr`). Tests fake
it; nothing in `control` imports gliderlabs.

The control plane is not a shell. Anything a client runs over ssh that
is not a shed command (terminal integrations that probe the remote host
with shell snippets, for example) is rejected by cobra as an unknown
command.

## The two transports

Both transports end in the same handler, so the command surface cannot
drift between them.

**Over TCP ssh.** OpenSSH connects to `127.0.0.1:2222` as user `shed`
with public-key auth. `shedd install` writes an ssh config stanza so
`ssh shed` resolves.

**Over the unix socket.** `bin/shed` dials `<state>/control.sock`,
runs an ssh client handshake over it (user `shed`, host key ignored),
opens one session, runs `shellquote.Join(os.Args[1:])` as the exec
line, wires its own stdin/stdout/stderr to the session, and exits with
the remote exit status. The socket server has no auth handlers at all;
gliderlabs then sets `NoClientAuth`. Being able to open a 0600 socket in
your own state directory is the authentication. Dial and handshake each
have a 5 s timeout; a failed dial prints a hint to start the daemon.

`bin/shed` deliberately contains no cobra tree. It is a dumb pipe. The
consequence: `shed --help` prints the daemon's help, and adding a
command means touching one place.

## Commands

All commands act on the in-process `vm.Manager`; none of them shell out.
Output helpers: `table` renders aligned columns with two-space gutters
and no trailing whitespace; `printJSON` emits two-space-indented JSON.

| Command | Semantics |
|---------|-----------|
| `new [name] [--image ref] [--cpu N] [--memory MB] [--disk GB] [--autostart] [--no-start] [--json]` | Creates a VM via `Manager.Create` with a 10 min context. Missing name is generated as `<adjective>-<noun>` from two 20-word lists, retried until unused. Zero-valued flags mean "use config default". Prints progress (pull, bake) to stdout as it happens, then the state, the ssh hint and the URL. |
| `ls [-l] [--json]` | Lists VMs sorted by creation time. Columns `NAME IMAGE STATE URL`; `-l` adds `CPU MEM DISK IP CREATED` and a trailing `pool:` line with used/total cpu, memory and disk. `--json` emits the full `vmspec.VM` records (an empty list, never `null`). |
| `rm <name>... [--json]` | `Manager.Remove` for each name in order; stops first if running; deletes the VM directory including the data disk. Stops at the first error. |
| `start|stop|restart <name>...` | Lifecycle verbs with a shared 5 min context. Prints `vm <name> is <state>` after each. |
| `cp <src> <dst> [--start]` | `Manager.Clone`; `--start` defaults to true and starts the clone afterwards. |
| `rename <old> <new>` | `Manager.Rename`; source must be stopped. |
| `resize <vm> [--cpu N] [--memory MB]` | `Manager.Resize`; VM must be stopped. An omitted flag keeps the current value; at least one is required. Disk is not resizable. Prints `vm <name>: N cpus, M MB memory`. |
| `share <vm>` | Prints the tokened URL that grants a browser access to a private VM. |
| `share set-public|set-private <vm>` | Flips `Share.Public`. |
| `share port <vm> <port>` | Sets the forwarded port (1..65535). |
| `share add <vm> <email>` | Records an email on the record for exe.dev parity; has no access effect locally, and says so. |
| `share ls <vm>` | Prints visibility, effective target port, and recorded emails. |
| `ssh-key ls` | Prints `<type> <SHA256 fingerprint>` per authorized key. |
| `ssh-key add <line>` | Validates one authorized_keys line and appends it. `-` reads the line from stdin. New keys reach guests on their next boot. |
| `whoami` | Prints the local macOS username and the authorized key count. |
| `browser <vm>` | Prints the plain URL. |
| `doc` | Prints a static text explaining shed. |
| `shelley` | Stub that fails with a message; exe.dev's web agent is out of scope. |

Errors returned from `RunE` are printed by cobra to the session's
stderr as `Error: <msg>`, and the session exits 1.

Output shapes worth knowing when scripting:

- `new --json` prints the human progress line `creating <name> from
  <image>...` (and any pull or bake progress) to **stdout before** the
  JSON record, so consumers must skip to the first `{`. The record is
  the full `vmspec.VM` (see [vm-lifecycle.md](vm-lifecycle.md)).
- `ls --json` is a JSON array of records, `[]` when empty.
- `rm --json` is `{"removed": ["a", "b"]}` after all removals succeed;
  a failure part-way returns the error and no JSON.
- `start|stop|restart`, `cp`, `rename`, `resize` have no `--json`; they
  print `vm <name> ...` lines.
- `share <vm>` and `browser <vm>` print a bare URL and nothing else.

## Design notes

**Why cobra bound to a session rather than a bespoke parser.** Free
help text, flag parsing, subcommands, and the ability to give the same
tree to tests with a fake session. The cost is that cobra's global-ish
defaults (completion command, usage on error) have to be switched off
explicitly.

**Why the client speaks ssh over a unix socket rather than a custom
RPC.** The gateway already implements sessions, exec lines, three
streams and exit codes. Reusing the ssh framing over the socket means
the socket path is exercised by the same handler and tests as the TCP
path, and `bin/shed` is ~60 lines.

**Why progress goes to stdout on `new` but stderr on `ssh box@shed`.**
`new` is a command whose stdout is the report. A brokered shell's stdout
belongs to the guest, so create-on-connect progress must not pollute it
(see [ssh-gateway.md](ssh-gateway.md)).

## Spec notes

- Exec line parsing: POSIX-shell-style word splitting (shellquote).
  Empty exec line means `help`.
- Exit code: 0 on success, 1 on any parse or command error. No other
  codes are produced by the control plane.
- `bin/shed`: dials the unix socket with a 5 s timeout, ssh user `shed`,
  host key not verified, exec line is `shellquote.Join(args)`, remote
  exit status becomes the process exit code; connection failures exit 1
  with a message on stderr prefixed `shed:`.
- Generated names: `<adjective>-<noun>` from fixed 20x20 word lists,
  must not collide with an existing VM.
- `--json` output is indented JSON, with `ls --json` always an array.
- `share port` accepts 1..65535 inclusive.
- `ssh-key add` validates with OpenSSH authorized_keys parsing before
  appending; the file is appended, never rewritten.
