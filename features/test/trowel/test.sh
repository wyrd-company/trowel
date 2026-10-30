#!/usr/bin/env bash
set -e
source dev-container-features-test-lib

TROWEL_HOME_DIR="$(getent passwd vscode | cut -d: -f6)"

check "trowel command exists globally" test -x /usr/local/bin/trowel
check "starter configuration is valid" env HOME="${TROWEL_HOME_DIR}" /usr/local/bin/trowel -config /etc/trowel/config.yml -validate
check "starter configuration listens on loopback" grep -qx '  listen: "127.0.0.1:8080"' /etc/trowel/config.yml
check "trowel launcher is executable" test -x /usr/local/bin/trowel-service
check "trowel launcher passes the config as a flag" grep -q '^exec /usr/local/bin/trowel -config /etc/trowel/config.yml ' /usr/local/bin/trowel-service
check "trowel service is registered" test -f /etc/s6-overlay/user-bundles.d/user/contents.d/trowel
check "trowel service is a longrun" grep -qx longrun /etc/s6-overlay/s6-rc.d/trowel/type
check "trowel service depends on base" test -f /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/base
check "trowel service runs as the automatic service user" grep -q '^exec s6-setuidgid vscode ' /etc/s6-overlay/s6-rc.d/trowel/run
check "trowel service receives the service user home" grep -q " HOME=${TROWEL_HOME_DIR} " /etc/s6-overlay/s6-rc.d/trowel/run
check "trowel service receives the service user name" grep -q ' USER=vscode ' /etc/s6-overlay/s6-rc.d/trowel/run
check "trowel launcher loads the conventional secret file" grep -Fq 'secret_file=/run/openbao/secrets/trowel.env' /usr/local/bin/trowel-service
check "trowel does not depend on an absent OpenBao Agent Feature" test ! -e /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/openbao-secrets
check "trowel does not depend on an absent Ansible Feature" test ! -e /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/ansible-setup
check "trowel Feature does not register systemd" test ! -e /etc/systemd/system/trowel.service

reportResults
