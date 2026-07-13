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
# Two subvolumes: @ (root, incl. /var/lib/dashboard state) and @nix (the store).
# Splitting them keeps the door open for the impermanence/erase-your-darlings
# plan later without a reformat.
{
  disko.devices.disk.main = {
    type = "disk";
    device = "/dev/vda";
    imageName = "ha-dashboard";
    imageSize = "10G";
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
                mountOptions = [ "compress=zstd" "noatime" ];
              };
              "/@nix" = {
                mountpoint = "/nix";
                mountOptions = [ "compress=zstd" "noatime" ];
              };
            };
          };
        };
      };
    };
  };
}
