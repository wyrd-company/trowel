#!/usr/bin/env bash
set -e
source dev-container-features-test-lib

check "ansible-setup service definition is present" test -d /etc/s6-overlay/s6-rc.d/ansible-setup
check "trowel waits for ansible-setup" test -f /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/ansible-setup
check "trowel still depends on base" test -f /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/base
check "trowel does not depend on an absent OpenBao Agent Feature" test ! -e /etc/s6-overlay/s6-rc.d/trowel/dependencies.d/openbao-secrets
check "trowel service is still a longrun" grep -qx longrun /etc/s6-overlay/s6-rc.d/trowel/type
check "trowel service is registered" test -f /etc/s6-overlay/user-bundles.d/user/contents.d/trowel

reportResults
