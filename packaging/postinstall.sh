#!/bin/sh
set -eu

# Fresh installs stay stopped until /etc/trowel/config.yml exists and the
# operator enables the unit. Upgrades restart a running relay on the new binary.
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl try-restart trowel.service
fi
