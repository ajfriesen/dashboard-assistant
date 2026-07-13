# Memory-pressure resilience for the Chromium kiosk. This is a RAM concern, not
# a disk one — it does NOT shrink the image or the store. The goal: a runaway
# browser degrades into a fast, clean session restart instead of freezing on
# disk swap or being OOM-killed uncleanly. Shared by the live ISO and the
# on-disk install (both run the same kiosk).
{ ... }:
{
  # Compressed swap in RAM. Chromium's anonymous pages compress well (~2-3x with
  # zstd), so this holds cold browser memory with zero writes to flash/eMMC —
  # the right kind of swap for an appliance on flash storage. It essentially
  # never activates when RAM is ample; it's a genuine cushion on low-RAM tablets.
  zramSwap = {
    enable = true;
    algorithm = "zstd";
    # max *uncompressed* data = 50% of RAM (actual RAM used is the compressed
    # size). Raise toward 100 for 2 GB-class tablets since the data is compressed.
    memoryPercent = 50;
  };

  # Proactive OOM handling: when memory pressure spikes, kill the offending
  # cgroup instead of letting the box thrash. For a single-app kiosk the offender
  # is Chromium, and killing its session makes greetd relaunch a fresh dashboard
  # — a ~2s blip, no flash writes, versus a frozen screen.
  #
  # enableUserSlices lets oomd act on the kiosk's graphical session (greetd opens
  # a logind session, so it lives under user-<uid>.slice). System services are
  # deliberately left unmanaged so oomd can never kill something load-bearing.
  systemd.oomd = {
    enable = true;
    enableUserSlices = true;
  };
}
