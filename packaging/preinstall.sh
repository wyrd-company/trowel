#!/bin/sh
set -eu

# Nothing to prepare. The unit runs as a systemd dynamic user, so no account
# is created, and the operator owns everything under /etc/trowel.
exit 0
