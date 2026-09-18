#!/bin/bash
# Build shed's guest kernel. Runs inside a sheduntu VM (make kernel does
# that for you); needs network, about 8 CPUs to finish in a few minutes,
# and ~15 GB of disk.
#
# Kata Containers' kernel configuration is the known-good base, rebuilt here:
# upstream stable Linux at the tag in ./versions, Kata's patches for that
# series applied, Kata's arm64 config fragments merged the way Kata's own
# build-kernel.sh does it, plus ./shed.conf. Output lands in $OUT:
#
#   Image          uncompressed arm64 kernel, what shedd boots
#   Image.sha256   the value to pin in internal/kernel
#   config         the .config it was built from, for diffing releases
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
. "$here/versions"

OUT=${OUT:-$HOME/kernel-out}
SRC=${SRC:-$HOME/kernel-src}
JOBS=${JOBS:-$(nproc)}
export ARCH=arm64
# With the patches committed, HEAD is no longer the upstream tag, and
# scripts/setlocalversion marks that with a trailing "+" unless
# LOCALVERSION is set in the environment. Empty keeps CONFIG_LOCALVERSION.
export LOCALVERSION=

step() { printf '\n==> %s\n' "$*"; }

step "installing the toolchain"
export DEBIAN_FRONTEND=noninteractive
sudo -E apt-get update -qq
sudo -E apt-get install -y -qq --no-install-recommends \
	build-essential bc bison flex libssl-dev libelf-dev git ca-certificates >/dev/null

mkdir -p "$SRC" "$OUT"
cd "$SRC"

step "fetching linux $LINUX"
if [ ! -d linux ]; then
	git clone -q --depth 1 -b "$LINUX" \
		https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git linux
fi
step "fetching kata-containers $KATA"
if [ ! -d kata ]; then
	git clone -q --depth 1 -b "$KATA" https://github.com/kata-containers/kata-containers.git kata
fi

kata_kernel=$SRC/kata/tools/packaging/kernel
fragments=$kata_kernel/configs/fragments
series=$(echo "${LINUX#v}" | cut -d. -f1-2).x

cd linux
git reset -q --hard "$LINUX" # pristine sources on a rerun

step "applying kata's patches for $series"
# Committed rather than left in the working tree: a dirty tree makes the
# kernel call itself 6.18.15-shed-dirty.
patches=$(find "$kata_kernel/patches/$series" -name '*.patch' 2>/dev/null | sort)
if [ -z "$patches" ]; then
	echo "no patches for $series (Kata carries none, or the series moved)"
fi
for p in $patches; do
	echo "  $(basename "$p")"
	patch -p1 --silent <"$p"
	git add -A
	git -c user.name=shed -c user.email=shed@localhost \
		commit -q -m "kata $KATA: $(basename "$p")"
done

step "merging kata's config fragments"
# -r: report redundancies. -n: start from allnoconfig, not alldefconfig,
# so that anything a fragment does not mention is off. Exactly what Kata's
# build-kernel.sh runs; shed.conf goes last so it wins.
conf_list="$(ls "$fragments"/common/*.conf "$fragments"/arm64/*.conf) $here/shed.conf"
# shellcheck disable=SC2086
merge_out=$(./scripts/kconfig/merge_config.sh -r -n $conf_list)
# merge_config complains about symbols that did not make it into the final
# config. Kata tolerates the ones in its whitelist (options dropped by
# newer kernels) and fails on anything else; so do we.
missing=$(grep 'not in final' <<<"$merge_out" | sed -E 's/^Value requested for ([A-Z0-9_]+) not in final.*/\1/' \
	| grep -v -x -F -f "$fragments/whitelist.conf" || true)
if [ -n "$missing" ]; then
	echo "fragments requested options the kernel does not have:" >&2
	echo "$missing" >&2
	exit 1
fi
make -s olddefconfig

step "building with $JOBS jobs"
make -s -j"$JOBS" Image

step "collecting output in $OUT"
cp arch/arm64/boot/Image "$OUT/Image"
cp .config "$OUT/config"
(cd "$OUT" && sha256sum Image | cut -d' ' -f1 >Image.sha256)
echo "release  $(make -s kernelrelease)"
echo "size     $(stat -c %s "$OUT/Image") bytes"
echo "sha256   $(cat "$OUT/Image.sha256")"
