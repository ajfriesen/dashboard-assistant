# Example local seed — per-build overrides for a known-network / dev image.
#
# Usage:
#   cp modules/local.example.nix modules/local.nix
#   $EDITOR modules/local.nix          # set your HA URL
#   git add modules/local.nix          # flakes only see git-tracked files!
#   just build-image
#
# flake.nix imports modules/local.nix automatically when it exists, so no other
# wiring is needed. Delete local.nix (and `git rm` it) to return to the
# interactive on-screen setup wizard.
#
# SECRETS: a LAN URL is fine to commit. Do NOT put credentials/tokens in here —
# a committed Nix file lands in the world-readable Nix store and in git history.
# Seed those via the device's runtime.env or a build-time secret instead.
{ ... }:
{
  # Home Assistant address to bake in. Presence of this value marks the device
  # "provisioned", so first boot goes straight to the dashboard and skips setup.
  dashboardAssistant.seed.haUrl = "http://homeassistant.local:8123";

  # DEV ONLY: expose Chromium remote debugging on 127.0.0.1:9222 so you can drive
  # the kiosk browser from host DevTools over `just qemu-ssh` (tunnels 9222).
  # Lets you paste a long token/password with the host clipboard. Leave off for
  # real images.
  # dashboardAssistant.debug.chromiumRemoteDebugging = true;

  # DEV ONLY: allow root SSH login with these keys (needed for `just qemu-ssh` /
  # `just net-check`; the live ISO has no root password). Paste your pubkey(s).
  # dashboardAssistant.debug.rootAuthorizedKeys = [
  #   "ssh-ed25519 AAAA... you@host"
  # ];
}
