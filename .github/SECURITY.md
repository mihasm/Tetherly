# Security policy

Tetherly runs a helper as root (it creates a `utun` interface, adds routes and publishes DNS for the tether link). Please report anything that lets another local user or process abuse it — the socket at `/var/run/tetherly.sock`, the helper's commands, files it writes — privately through GitHub's "Report a vulnerability" button on this repository rather than in a public issue.

What the helper accepts from unprivileged clients: `status`, `logs`, `get_config`, `version`. `connect`, `disconnect` and `set_config` need root, the admin group or the console user (checked with `LOCAL_PEERCRED`).
