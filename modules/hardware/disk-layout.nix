# Declarative disk layout for the persistent install, consumed by disko. This
# replaces the ext4 root that make-disk-image produced (make-disk-image can only
# emit ext4 for a partitioned image). btrfs with `compress=zstd` roughly halves
# the on-disk footprint of the Nix store — the single biggest space win for an
# appliance nobody prunes by hand.
#
# `device` is only the default target used when building the image; a real
# install onto someone else's board overrides it, e.g.
#   disko-install --flake .#dashboard-x86-disk --disk main /dev/nvme0n1
#
# Two subvolumes: @ (root, incl. /var/lib/dashboard-assistant state) and @nix (the store).
# Splitting them keeps the door open for the impermanence/erase-your-darlings
# plan later without a reformat.
{
  disko.devices.disk.main = {
    type = "disk";
    device = "/dev/vda";
    imageName = "dashboard-assistant";
    # Small image: just big enough for the initial closure. It's grown to fill
    # the real disk on first boot (boot.growPartition in generic-x86-disk.nix +
    # the x-systemd.growfs mount option on / below), so flashing to a 16 GB
    # tablet or a 500 GB SSD both end up using the whole disk.
    imageSize = "4G";
    content = {
      type = "gpt";
      partitions = {
        ESP = {
          priority = 1;
          name = "ESP";
          start = "1M";
          end = "512M";
          type = "EF00";
          content = {
            type = "filesystem";
            format = "vfat";
            mountpoint = "/boot";
            mountOptions = [ "umask=0077" ];
          };
        };
        root = {
          size = "100%";
          content = {
            type = "btrfs";
            extraArgs = [ "-f" ];
            subvolumes = {
              "/@" = {
                mountpoint = "/";
                # x-systemd.growfs: after the partition is grown, expand the
                # btrfs to fill it (one resize grows the whole fs incl. @nix).
                mountOptions = [
                  "compress=zstd"
                  "noatime"
                  "x-systemd.growfs"
                ];
              };
              "/@nix" = {
                mountpoint = "/nix";
                mountOptions = [
                  "compress=zstd"
                  "noatime"
                ];
              };
            };
          };
        };
      };
    };
  };
}
