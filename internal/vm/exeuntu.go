package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fredrik/shed/internal/backend"
	"github.com/fredrik/shed/internal/diskfs"
	"github.com/fredrik/shed/internal/vm/vmspec"
	"github.com/fredrik/shed/internal/vsockproto"
)

// exeuntu is shed's default image, in the spirit of exe.dev's: Ubuntu
// with the tools you expect already installed. It is baked locally, once:
// a throwaway VM boots the upstream Ubuntu image, runs the recipe below,
// and streams its merged rootfs back to become a cached base image.
// The cache key covers only the version and the recipe, so a baked image
// resolves offline — no registry round trip on the create path. Upstream
// Ubuntu updates are picked up by bumping exeuntuVersion.

const (
	exeuntuName    = "exeuntu"
	exeuntuBase    = "ubuntu:24.04"
	exeuntuVersion = "v1" // bump to force a rebake (e.g. for upstream Ubuntu updates)

	exeuntuScript = `set -eux
# The Ubuntu OCI image drops /usr/share/doc/*, and that is where Debian
# ships fzf's shell integration. Keep fzf's examples, nothing else. (Read
# after the image's own "excludes" file, which is why the zz- name.)
echo 'path-include=/usr/share/doc/fzf/examples/*' > /etc/dpkg/dpkg.cfg.d/zz-fzf-examples

apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates curl wget git vim nano less htop tmux ncurses-term \
  ripgrep jq unzip zip file rsync openssh-client sudo \
  iproute2 iputils-ping dnsutils netcat-openbsd python3 \
  zsh zsh-autosuggestions zsh-syntax-highlighting \
  fzf bat fd-find zoxide tree
apt-get clean

# Ubuntu renames these two to dodge name clashes; put the names everyone
# actually types on the PATH.
ln -sf /usr/bin/batcat /usr/local/bin/bat
ln -sf /usr/bin/fdfind /usr/local/bin/fd

# On-disk check, recorded in the bake log: dpkg -L would list these even
# when they were never unpacked, which is the whole trap above.
ls -l /usr/share/doc/fzf/examples/*.zsh || true

# Toolchain managers, system-wide so scripts and cron see them without
# shell activation: the image owns the machine, mise owns the work.
curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh
curl -LsSf https://astral.sh/uv/install.sh | \
  UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 INSTALLER_NO_MODIFY_PATH=1 sh

# The prompt: one static binary, renders in a millisecond, and reads the
# mise-managed toolchain versions on its own.
curl -sS https://starship.rs/install.sh | sh -s -- -y -b /usr/local/bin

# Skel dotfiles (written before useradd so the dev user inherits them):
# mise activation for interactive shells, project configs under $HOME
# trusted, node 24 as the global tool, uv tuned for ext4.
mkdir -p /etc/skel/.config/mise /etc/skel/.config/uv /etc/skel/.config/tmux
# Pre-seed the "sudo lecture done" marker so Ubuntu's bash.bashrc does not
# append its "man sudo_root" hint after our motd on every login.
touch /etc/skel/.sudo_as_admin_successful
cat >> /etc/skel/.bashrc <<'RC'

# shed: mise manages per-project tools
if command -v mise >/dev/null 2>&1; then
  eval "$(mise activate bash)"
fi
RC
cat > /etc/skel/.config/mise/config.toml <<'TOML'
[settings]
trusted_config_paths = ["~"]

[tools]
node = "24"
TOML
cat > /etc/skel/.config/uv/uv.toml <<'TOML'
python-preference = "managed"
link-mode = "hardlink"
compile-bytecode = true
TOML

# The login shell. Short enough to read in one screen, and every line is
# something you would have added yourself: no framework, no plugin manager.
# Distro-provided files are sourced defensively -- packaging moves them
# between releases, and a missing one must not break the shell.
cat > /etc/skel/.zshrc <<'RC'
# exeuntu defaults -- yours to edit or delete.

# History: big, shared between sessions, deduped.
HISTFILE=~/.zsh_history
HISTSIZE=100000
SAVEHIST=100000
setopt share_history hist_ignore_all_dups hist_ignore_space

setopt auto_cd interactive_comments no_beep

# Completion. -C trusts the cached dump (pre-built in the image) instead of
# rescanning every directory on the fpath at every login.
autoload -Uz compinit && compinit -C
zstyle ':completion:*' menu select
zstyle ':completion:*' matcher-list 'm:{a-z}={A-Za-z}'

# Prompt.
command -v starship >/dev/null && eval "$(starship init zsh)"

# shed: mise manages per-project tools
command -v mise >/dev/null && eval "$(mise activate zsh)"

# Fuzzy everything: ctrl-r through history, ctrl-t through files.
for _f in /usr/share/doc/fzf/examples/key-bindings.zsh \
          /usr/share/doc/fzf/examples/completion.zsh; do
  [ -r "$_f" ] && source "$_f"
done
unset _f

# z <dir> jumps to your most-used directories.
command -v zoxide >/dev/null && eval "$(zoxide init zsh)"

# Greyed-out suggestions from history, then colour-as-you-type. The
# highlighter wraps the line editor, so it has to be the last thing loaded.
[ -r /usr/share/zsh-autosuggestions/zsh-autosuggestions.zsh ] &&
  source /usr/share/zsh-autosuggestions/zsh-autosuggestions.zsh
[ -r /usr/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh ] &&
  source /usr/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh

alias ls='ls --color=auto -F'
alias ll='ls -lah'
alias grep='grep --color=auto'
RC

# Plain unicode only: these glyphs render in the host's terminal, which we
# do not control, so the default has to look right everywhere.
cat > /etc/skel/.config/starship.toml <<'TOML'
# exeuntu prompt. Nerd Font in your terminal? One command upgrades it:
#   starship preset nerd-font-symbols -o ~/.config/starship.toml
format = """
$directory$git_branch$git_status$nodejs$python$golang$rust$cmd_duration
$character"""

[character]
success_symbol = "[>](bold green)"
error_symbol = "[>](bold red)"

[directory]
style = "bold cyan"
truncation_length = 3
truncate_to_repo = true

[git_branch]
symbol = "git:"
style = "bold magenta"

[git_status]
style = "bold yellow"

[cmd_duration]
min_time = 2000
style = "yellow"

[nodejs]
symbol = "node "
[python]
symbol = "py "
[golang]
symbol = "go "
[rust]
symbol = "rs "
TOML

cat > /etc/skel/.config/tmux/tmux.conf <<'CONF'
# exeuntu defaults -- yours to edit or delete.
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",*256col*:Tc,xterm*:Tc"
set -g mouse on
set -sg escape-time 10
set -g history-limit 100000
set -g base-index 1
setw -g pane-base-index 1
set -g renumber-windows on
CONF

# The default login user: uid 1000, zsh, passwordless sudo. The stock
# ubuntu user makes way so uid 1000 stays conventional.
userdel -r ubuntu 2>/dev/null || true
useradd -m -u 1000 -s /usr/bin/zsh dev
usermod -aG sudo dev
echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev
chmod 440 /etc/sudoers.d/dev

# Pre-warm the global toolchain into the image (bake-time cost, zero
# boot-time cost): node 24 from the seeded mise config, and a uv-managed
# python 3.14 as dev's default python/python3 (system python3 from apt
# stays the baseline at /usr/bin/python3).
# -s /bin/bash: dev's login shell is zsh now, and the bake must not depend
# on the new startup files being correct to install the toolchain.
su - dev -s /bin/bash -c 'mise install'
su - dev -s /bin/bash -c 'uv python install --default 3.14 || uv python install 3.14'

# Build the completion dump at bake time so the first real login pays
# nothing for it. Best-effort: there is no tty here.
su - dev -s /bin/bash -c 'zsh -ic exit' || true

# printf %b for the escapes: the agent writes this file verbatim, and only
# to pty sessions, so the colour is safe.
printf '%b' '
  \033[1;36mexeuntu\033[0m \033[2m-- Ubuntu 24.04, shed build\033[0m

  This microVM is yours: persistent disk, apt works, sudo is free.
  Web port proxied at  \033[4;34mhttp://<vmname>.shed.localhost:8080\033[0m
  Fleet from the host: \033[1mssh shed help\033[0m

' > /etc/motd
`
)

func isExeuntu(ref string) bool {
	return ref == exeuntuName || ref == exeuntuName+":latest"
}

// ensureImage resolves any image reference, treating exeuntu specially.
func (m *Manager) ensureImage(ctx context.Context, ref string, progress io.Writer) (vmspec.ImageInfo, string, error) {
	if isExeuntu(ref) {
		return m.ensureExeuntu(ctx, progress)
	}
	return m.prep.EnsureImage(ctx, ref)
}

func (m *Manager) ensureExeuntu(ctx context.Context, progress io.Writer) (vmspec.ImageInfo, string, error) {
	sum := sha256.Sum256([]byte(exeuntuVersion + "\x00" + exeuntuScript))
	tag := hex.EncodeToString(sum[:])[:12]
	imgPath := filepath.Join(m.cfg.CacheDir, "base", "exeuntu-"+tag+".img")
	infoPath := imgPath + ".json"

	if info, err := loadImageInfo(infoPath); err == nil {
		if _, err := os.Stat(imgPath); err == nil {
			return info, imgPath, nil
		}
	}

	_, basePath, err := m.prep.EnsureImage(ctx, exeuntuBase)
	if err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("exeuntu base %s: %w", exeuntuBase, err)
	}

	fmt.Fprintf(progress, "baking the exeuntu image (first time only, a few minutes)...\n")

	bakeDir, err := os.MkdirTemp("", "shed-bake-*")
	if err != nil {
		return vmspec.ImageInfo{}, "", err
	}
	defer os.RemoveAll(bakeDir)
	dataDisk := filepath.Join(bakeDir, "data.img")
	if err := m.prep.EnsureDataDisk(dataDisk, 8); err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("bake data disk: %w", err)
	}

	bctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	serialLog := filepath.Join(m.cfg.CacheDir, "exeuntu-bake.log")
	run, err := m.be.Start(bctx, backend.StartRequest{
		Spec: vmspec.Spec{
			Name: "exeuntu-bake", Image: exeuntuBase,
			CPUs: 2, MemoryMB: 2048, DiskGB: 8,
			Created: time.Now().UTC(),
		},
		BaseDiskPath:  basePath,
		DataDiskPath:  dataDisk,
		KernelPath:    m.kernel,
		SerialLogPath: serialLog,
		GuestConfig: vsockproto.Config{
			Hostname:       "exeuntu",
			AuthorizedKeys: m.guestKeys(),
			BakeScript:     exeuntuScript,
		},
	})
	if err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("bake vm: %w (serial log: %s)", err, serialLog)
	}
	defer run.Kill()

	conn, err := run.DialGuest(bctx, vsockproto.BakeTarPort)
	if err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("fetch baked rootfs: %w", err)
	}
	defer conn.Close()

	fmt.Fprintf(progress, "harvesting baked rootfs into base image...\n")
	if err := diskfs.BuildBaseDisk(conn, imgPath, 16*1024*1024*1024); err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("build exeuntu base: %w", err)
	}

	info := vmspec.ImageInfo{
		Digest: "exeuntu:" + tag,
		Env:    []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cmd:    []string{"/bin/bash"},
	}
	if err := saveImageInfo(infoPath, info); err != nil {
		os.Remove(imgPath)
		return vmspec.ImageInfo{}, "", err
	}

	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	run.Shutdown(shutdownCtx)

	pruneOldExeuntu(filepath.Dir(imgPath), tag)
	return info, imgPath, nil
}

// pruneOldExeuntu removes superseded exeuntu base images. Safe: VMs always
// resolve "exeuntu" to the current bake on start, so older ones are
// orphaned the moment a new bake lands.
func pruneOldExeuntu(dir, keepTag string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "exeuntu-*.img"))
	for _, m := range matches {
		if m == filepath.Join(dir, "exeuntu-"+keepTag+".img") {
			continue
		}
		os.Remove(m)
		os.Remove(m + ".json")
	}
}

func loadImageInfo(path string) (vmspec.ImageInfo, error) {
	var info vmspec.ImageInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(data, &info)
}

func saveImageInfo(path string, info vmspec.ImageInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
