#!/usr/bin/env bash
set -e
source dev-container-features-test-lib

check "trowel waits for rendered secrets" test -f /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/openbao-secrets
check "trowel user can read rendered secret files" bash -c 'id -nG vscode | tr " " "\n" | grep -qx openbao-secrets'

reportResults
