# About

**Dashboard Assistant OS** turns any spare tablet, mini-PC or single-board
computer into a dedicated Home Assistant wall panel. It is a minimal, declarative
NixOS system that boots straight into a full-screen browser locked to your
dashboard — no desktop, no login screen, nothing to tap through.

## Why another kiosk OS?

Most "wall tablet" setups are a stack of manual tweaks: disable the lock screen,
side-load a kiosk browser, fight the OS updater, and hope none of it breaks after
a reboot. Dashboard Assistant takes the opposite approach:

- **Declarative and reproducible.** The whole system is defined in NixOS. The
  image you flash is the system you run — there is no hand-configuration to drift.
- **Unbreakable by design.** Updates are atomic. A boot that fails its health
  check automatically rolls back to the previous working generation, and a
  recovery picker lets you pick an older one by hand.
- **A first-class Home Assistant citizen.** The panel doesn't just *show* Home
  Assistant — it reports back over MQTT as a device with its own controls and
  sensors (display, brightness, zoom, screenshots, CPU, temperature and more).

## Project status

Dashboard Assistant is under active development. x86_64 is the primary, tested
target today; Raspberry Pi and other aarch64 boards are a work in progress. See
[Hardware Support](../hardware-support.md) for the current state.

The project is open source on
[GitHub](https://github.com/ajfriesen/dashboard-assistant) — issues and pull
requests are welcome.
