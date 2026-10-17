#!/bin/sh
set -eu

# Debian and AUR pass "remove", RPM passes 0. Upgrades leave the unit alone.
case "${1:-}" in
remove | 0)
	if [ -d /run/systemd/system ]; then
		systemctl disable --now trowel.service
	fi
	;;
esac
