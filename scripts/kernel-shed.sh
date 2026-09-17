#!/bin/sh
# shed client for the daemon started by kernel-daemon.sh. Without the
# matching state dir, bin/shed would quietly talk to the live daemon.
set -eu
SHED_STATE_DIR=${SHED_KERNEL_STATE:-/tmp/shed-kdaemon} exec "$(dirname "$0")/../bin/shed" "$@"
