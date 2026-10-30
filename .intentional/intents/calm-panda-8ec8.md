---
trowel: major
trowel-docs: patch
---

Start the devcontainer Feature's Trowel service only after the Ansible Feature's ansible-setup s6-rc oneshot succeeds when that service is installed, and install after the Ansible Feature. Without the Ansible Feature, Trowel starts as before, and the OpenBao Agent dependency stays independent.

<!-- bump translation: contract-1 `minor` for `trowel` at 0.2.0 is `major` here, so that the next version stays 0.3.0. -->
