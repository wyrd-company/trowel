---
trowel: minor
trowel-docs: patch
---

Read configuration snippets from the conf.d directory beside the config file, such as /etc/trowel/conf.d. Their webhooks and subjects are added in file name order, a snippet that sets server, nats, or vault already set elsewhere is an error, and Trowel reloads when a snippet is added, changed, or removed. DEB and RPM packages and the devcontainer Feature create an empty /etc/trowel/conf.d.
