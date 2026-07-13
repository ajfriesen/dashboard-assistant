# Generic x86_64 hardware profile.
#
# Delivery this iteration: a live ISO. The ISO runs a read-only squashfs root
# with a tmpfs overlay, so the system is inherently ephemeral / un-brickable —
# this satisfies the "ephemeral root" goal without an explicit tmpfs `/` or a
# `/persist` partition. On-disk install with real impermanence is deferred.
{ modulesPath, ... }:
{
  imports = [
    "${modulesPath}/installer/cd-dvd/iso-image.nix"
  ];

  nixpkgs.hostPlatform = "x86_64-linux";

  # Bootable from EFI systems and when dd'd to a USB stick.
  isoImage.makeEfiBootable = true;
  isoImage.makeUsbBootable = true;

  # Broad out-of-the-box hardware/Wi-Fi support on generic devices.
  hardware.enableRedistributableFirmware = true;
}
