#!/bin/sh
set -eu

# Nothing to prepare. The unit runs as a systemd dynamic user, so no account
# is created. The package ships the empty /etc/trowel/conf.d directory; the
# operator owns every file under /etc/trowel.
exit 0
