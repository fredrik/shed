#!/bin/sh
# Run a throwaway shedd beside the live one, booting VMs on a kernel Image
# of your own. Ports 2223 (ssh) and 8081 (http), state under /tmp, so the
# live daemon's records and control socket are never touched.
#
#   scripts/kernel-daemon.sh bin/kernel/Image     # terminal 1
#   scripts/kernel-shed.sh new ktest              # terminal 2
#   ssh -p 2223 ktest@127.0.0.1
#   scripts/kernel-shed.sh rm ktest
#
# Run with no argument to boot the pinned kernel instead: the control
# case, worth having before trusting a failure to your build.
#
# SHED_KERNEL_STATE, SHED_KERNEL_SSH and SHED_KERNEL_HTTP move the state
# dir and ports, for a second throwaway daemon beside the first.
set -eu

state=${SHED_KERNEL_STATE:-/tmp/shed-kdaemon}
mkdir -p "$state"
cat >"$state/config.toml" <<TOML
ssh_addr = "127.0.0.1:${SHED_KERNEL_SSH:-2223}"
http_addr = "127.0.0.1:${SHED_KERNEL_HTTP:-8081}"
TOML

if [ $# -gt 0 ]; then
	SHED_KERNEL=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
	export SHED_KERNEL
	echo "booting VMs on $SHED_KERNEL"
else
	echo "booting VMs on the pinned kernel"
fi
SHED_STATE_DIR=$state exec "$(dirname "$0")/../bin/shedd" serve
