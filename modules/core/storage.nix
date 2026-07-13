# Runtime storage management for the *persistent* install (imported by the disk
# target, NOT by the live ISO — the ISO's /nix/store is a read-only squashfs, so
# GC there would only error).
#
# The premise of this appliance is that nobody ever opens a terminal to run
# `nix-collect-garbage`. Without that, every update kept forever is the classic
# way a NixOS box slowly fills its disk. These three levers make pruning,
# dedup, and a hard low-disk floor fully automatic.
{ ... }:
{
  # 1. Garbage-collect old generations on a timer. `persistent` catches up on
  #    the next boot if the box was powered off when the timer was due (an
  #    appliance may be off for stretches). Two weeks leaves a little rollback
  #    history without letting the store grow unbounded; tighten if disk is tight.
  nix.gc = {
    automatic = true;
    dates = "weekly";
    persistent = true;
    options = "--delete-older-than 14d";
  };

  # 2. Deduplicate the store by hard-linking identical files. Enabled inline so
  #    every path written (image build + every future update) is deduped as it
  #    lands — typically a 20-30% smaller store, for free.
  nix.settings.auto-optimise-store = true;

  # 3. Hard safety net, independent of the GC timer: whenever free space drops
  #    below min-free during ANY store operation (e.g. pulling an update), the
  #    nix-daemon collects garbage until max-free is available. This is what
  #    guarantees an unattended device can never fill its disk to 0% mid-update.
  nix.settings.min-free = 512 * 1024 * 1024; # start GC below 512 MiB free
  nix.settings.max-free = 2 * 1024 * 1024 * 1024; # free up to 2 GiB
}
