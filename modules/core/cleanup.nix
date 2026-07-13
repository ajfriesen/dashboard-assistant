# Image slimming for the appliance: strip documentation we never use on a
# headless kiosk (man/info/nixos manual/docs). Keeps the ISO smaller and the
# build faster.
{ ... }:
{
  documentation = {
    enable = false;
    nixos.enable = false;
    man.enable = false;
    info.enable = false;
    doc.enable = false;
  };
}
