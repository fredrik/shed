# Plan: exeuntu v2 — zsh, starship, and a lovely logged-in experience

## Context

The logged-in shed experience is currently stock Ubuntu: bash, default
prompt, plain motd. Goals for v2 of the exeuntu image:

- **zsh** as the default login shell instead of bash.
- **Snazzy and gorgeous default look** — prompt, colors, motd.
- **Lovely dx** — the shell should feel like a dotfiles-nerd's machine on
  first ssh, with so little config that `cat ~/.zshrc` fits on one screen
  and explains itself.

Everything below is recipe edits in `internal/vm/exeuntu.go` (the
`exeuntuScript` const) plus a README paragraph. The cache key hashes the
recipe, so any edit rebakes automatically on next use — no
`exeuntuVersion` bump needed. All config lives in `/etc/skel` +
`/usr/local/bin`; no frameworks, nothing that needs network at boot.

## Decisions

- **No oh-my-zsh.** Slow, mostly dead weight. Hand-rolled `~/.zshrc`
  (~40 lines) plus two apt plugins gets 90% of the feel at ~0 startup
  cost.
- **Starship prompt**, not a zsh theme: single static binary, ~ms render,
  cross-shell (works if someone drops to bash), reads mise-managed
  node/python automatically.
- **Plain-unicode symbols** in the starship config, not a Nerd Font
  preset. Glyphs render in the *host* terminal, which we don't control;
  the default must look right everywhere. README notes that Nerd Font
  users can swap in a fancier preset with one command inside the VM.
- **apt-only toolbelt** where possible. `eza` is not in 24.04's repos —
  skip it rather than adding a third-party source (revisit; could come
  via mise global tools).
- **Root stays bash.** `sudo -i` is the escape hatch, and root sessions
  are for surgery, not living in. (Cheap to revisit: point root's shell
  at the same zshrc.)
- **`Cmd: ["/bin/bash"]` in the baked ImageInfo stays.** That's the
  supervised process, not the login shell; the gateway runs the shell
  from `/etc/passwd`.

## Recipe changes

### 1. zsh as the login shell

- Add `zsh zsh-autosuggestions zsh-syntax-highlighting` to the apt list.
- `useradd -m -u 1000 -s /usr/bin/zsh dev` (was `-s /bin/bash`).
- The gateway already execs the `/etc/passwd` shell as a login shell in
  `$HOME`, so nothing outside the recipe changes.

### 2. `/etc/skel/.zshrc`

Sketch (final version to taste):

```zsh
# exeuntu defaults — yours to edit or delete.

# History: big, shared, deduped
HISTFILE=~/.zsh_history HISTSIZE=100000 SAVEHIST=100000
setopt share_history hist_ignore_all_dups hist_ignore_space

# Completion
autoload -Uz compinit && compinit
zstyle ':completion:*' menu select

# Prompt
eval "$(starship init zsh)"

# mise manages per-project tools
command -v mise >/dev/null && eval "$(mise activate zsh)"

# Fuzzy everything: ctrl-r history, ctrl-t files
source /usr/share/doc/fzf/examples/key-bindings.zsh
source /usr/share/doc/fzf/examples/completion.zsh

# z <dir> jumps to your most-used directories
eval "$(zoxide init zsh)"

# Autosuggestions + syntax highlighting (highlighting must load last)
source /usr/share/zsh-autosuggestions/zsh-autosuggestions.zsh
source /usr/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh

alias ls='ls --color=auto -F' ll='ls -lah' grep='grep --color=auto'
```

Keep the existing `.bashrc` mise hook so bash still works when chosen.

### 3. Starship

- Install via its official script to `/usr/local/bin` (same pattern as
  the existing mise/uv installs): `curl -sS https://starship.rs/install.sh
  | sh -s -- -y -b /usr/local/bin`.
- `/etc/skel/.config/starship.toml`: directory, git branch/status,
  language versions, command duration, exit-status coloring; symbols
  restricted to safe unicode (no Nerd Font glyphs).

### 4. Toolbelt

- Add to apt list: `fzf bat fd-find zoxide tree`.
- Symlink Ubuntu's renamed binaries: `batcat`→`/usr/local/bin/bat`,
  `fdfind`→`/usr/local/bin/fd`.
- `cat` stays `cat`; `bat` is simply available.

### 5. Snazzier arrival

- Colorized `/etc/motd`: ANSI escapes work as-is (the agent prints the
  file verbatim on interactive connects). Small tinted "exeuntu"
  wordmark + the two useful lines (proxy URL, `ssh shed help`).
- `/etc/skel/.config/tmux/tmux.conf`: truecolor
  (`terminal-overrides ,*:Tc`), `set -g mouse on`, sane escape-time —
  so tmux inside a VM doesn't look like 1995.

### 6. README

Update the exeuntu paragraph: zsh + starship + the toolbelt, and the
Nerd Font note.

## Costs

- Bake time: +20–30 s (a handful of apt packages + one binary download).
- Image size: +tens of MB.
- Rebake is automatic on next use (recipe changes the cache key); old
  bakes are pruned.

## Open taste decisions

- Nerd Font symbols on by default vs. the safe-unicode default chosen
  above.
- eza: skip (current call), via mise, or third-party apt source.
- Give root the same zsh setup, or leave root on bash (current call).
- Alias `cat`→`bat`? (Current call: no — surprising in scripts and
  pipes-that-aren't.)
