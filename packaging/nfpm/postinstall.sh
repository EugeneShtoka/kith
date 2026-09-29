#!/bin/sh
# Post-install note for GoReleaser nfpm packages. Prints only: the daemon is a
# systemd *user* unit and package scripts run as root.

if command -v systemctl >/dev/null 2>&1; then
	cat <<'NOTE'

kith is installed: kith (client), kithd (daemon), kith-mcp (MCP server).

Set it up as your own user, not root:

  kith                     # first run writes ~/.config/kith/config.toml; edit it
  kith login               # store the session in the keyring (Secret Service)
  systemctl --user daemon-reload
  systemctl --user import-environment WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS
  systemctl --user enable --now kithd          # or kithd@<profile> per profile

Upgrading? Restart the running daemon so it runs the new binary:

  systemctl --user restart kithd
NOTE
else
	cat <<'NOTE'

kith is installed: kith (client), kithd (daemon), kith-mcp (MCP server).

This system has no systemd, so the bundled user units will not be used. As your
own user, not root:

  kith                     # first run writes ~/.config/kith/config.toml; edit it
  kith login               # store the session in the keyring (Secret Service)
  kith                     # starts kithd itself (needs setsid), or
                             # run kithd from your session's autostart
NOTE
fi

cat <<'NOTE'

Optional, for the features that use them: a Secret Service keyring (gnome-keyring,
KWallet or KeePassXC), a notification daemon, xdg-desktop-portal (file picker),
mpv or vlc (voice messages), wl-clipboard or xclip (auto-copy), hunspell (spelling).
Guide: /usr/share/doc/kith/docs/getting-started.md

NOTE
