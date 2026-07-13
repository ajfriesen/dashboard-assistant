# On-disk install target (e.g. ODROID H2 with a SATA SSD).
#
# Unlike the live ISO, this is a persistent system with a real bootloader, so it
# boots from a fixed SATA disk, survives reboots (token/state persist), and can
# be updated in place with `nixos-rebuild switch`.
{ lib, ... }:
{
  nixpkgs.hostPlatform = "x86_64-linux";
  hardware.enableRedistributableFirmware = true;

  # GRUB EFI installed at the *removable* fallback path (\EFI\BOOT\BOOTX64.EFI).
  # ODROID H2-class firmware won't auto-boot a bootloader from a fixed SATA disk
  # via an NVRAM entry alone, but it does honour the removable fallback — the
  # same mechanism that makes USB media boot. This is the crux of the fix.
  boot.loader.grub = {
    enable = true;
    efiSupport = true;
    efiInstallAsRemovable = true;
    devices = [ "nodev" ];
  };
  boot.loader.efi.canTouchEfiVariables = false;

  # Enough to bring up SATA/NVMe/USB storage and HID in early boot.
  boot.initrd.availableKernelModules = [
    "ahci"
    "ata_piix"
    "xhci_pci"
    "ehci_pci"
    "nvme"
    "usb_storage"
    "sd_mod"
    "usbhid"
  ];
  boot.kernelModules = [ "kvm-intel" ];

  # Labels match what make-disk-image's "efi" layout creates (see flake.nix).
  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
  };
  fileSystems."/boot" = {
    device = "/dev/disk/by-label/ESP";
    fsType = "vfat";
  };
}
