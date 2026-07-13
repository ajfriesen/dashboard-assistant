# Image slimming for the appliance: strip things a headless single-app kiosk
# never uses. Keeps the image smaller and the build faster.
{ lib, pkgs, ... }:
{
  # Docs we never read on an appliance (man/info/nixos manual/HTML docs).
  documentation = {
    enable = false;
    nixos.enable = false;
    man.enable = false;
    info.enable = false;
    doc.enable = false;
  };

  # Speech synthesis. Something in the graphical stack pulls speech-dispatcher
  # into the system path, which drags in espeak-ng + flite + freepats and the
  # ~645 MiB mbrola-voices set — roughly 800 MiB total. The kiosk (Chromium on
  # HA) does no client-side Web Speech synthesis, so cut the whole chain.
  services.speechd.enable = lib.mkForce false;

  # Fonts: the default set ships Noto CJK sans+serif (~115 MiB) and CJK isn't
  # used by a Latin-script HA dashboard. Replace the defaults with a minimal set
  # that still gives Chromium sans/serif/mono coverage plus colour emoji.
  fonts.enableDefaultPackages = false;
  fonts.packages = with pkgs; [
    dejavu_fonts
    liberation_ttf
    noto-fonts-color-emoji
  ];

  # No cellular modem on this hardware — stop NetworkManager from launching
  # ModemManager. (Runtime only; NetworkManager still references the package.)
  systemd.services.ModemManager.enable = false;
}
