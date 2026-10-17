#!/usr/bin/env bash
set -e
source dev-container-features-test-lib

check "starter configuration is valid" /usr/local/bin/trowel -config /etc/trowel/config.yml -validate
check "listen option reaches the configuration" grep -qx '  listen: "0.0.0.0:9090"' /etc/trowel/config.yml
check "natsUrl option reaches the configuration" grep -qx '  url: "nats://sample-nats.example.test:4222"' /etc/trowel/config.yml
check "trowel service runs as root" grep -q '^exec s6-setuidgid root ' /etc/s6-overlay/s6-rc.d/trowel/run

reportResults
