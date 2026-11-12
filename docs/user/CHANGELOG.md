# Changelog

## 0.1.1

### Fixes

- Start the devcontainer Feature's Trowel service only after the Ansible Feature's ansible-setup s6-rc oneshot succeeds when that service is installed, and install after the Ansible Feature. Without the Ansible Feature, Trowel starts as before, and the OpenBao Agent dependency stays independent.
  
  <!-- bump translation: contract-1 `minor` for `trowel` at 0.2.0 is `major` here, so that the next version stays 0.3.0. -->
- Read configuration snippets from the conf.d directory beside the config file, such as /etc/trowel/conf.d. Their webhooks and subjects are added in file name order, a snippet that sets server, nats, or vault already set elsewhere is an error, and Trowel reloads when a snippet is added, changed, or removed. DEB and RPM packages and the devcontainer Feature create an empty /etc/trowel/conf.d.
- Publish the public docs from docs/user in the wyrd.foo format, and point the demo recorder at docs/user/assets.
