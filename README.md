# Dashbord Assistant OS

This is 

## Goals

- Easy Flash and Install, no Linux knowledge needed
- Integreates with Home Assistant via MQTT
- Unbreakable System via NixOS (A bad update will boot into the last working version)
- Support for x86, Raspberry Pi (Comming Soon, do not have a Pi at the moment)
- 

- easy install
- easy updates
- integrates with home assistant
- allows to configure multiple urls
- wake up by touch
- control:
  - display on/off
  - display brightness
  - touch screen
  - new update sensor
  - update via Home Assistant or GUI
  - manage zoom
  - take screenshot display
  - show ip
  - volume control
  - reboot, shutdown
- montior:
  - battery
  - temperature
  - cpu
  - memory
  - storage
  - 

## Hardware support

The OS is a **Chromium kiosk on NixOS**. That imposes three hard requirements, and
everything below follows from them:

1. **64-bit arch with a binary cache** — `x86_64` or `aarch64`. 32-bit ARM
   (`armv7`) is uncached/marginal; **ARMv6** (Pi 1, Pi Zero) is unsupported by
   nixpkgs — no cache means compiling everything (incl. Chromium) from source,
   which is effectively impossible on those boards.
2. **≥ 2 GB RAM** — Chromium rendering a Home Assistant dashboard (plus Sway,
   waybar, the on-screen keyboard and the daemon) needs it. 1 GB is marginal;
   under 1 GB will OOM.
3. **A GPU with a mainline mesa/KMS driver** — for smooth Wayland/Chromium.
   Boards that fall back to software rendering are too slow for a live dashboard.

### Status by device class

| Device class | Arch | RAM | Status | Notes |
|---|---|---|---|---|
| x86_64 mini-PC / thin client / tablet | `x86_64` | ≥ 2 GB | ✅ Supported | Primary target. Broad driver support, single disk image. |
| Raspberry Pi 4 / 5 / 400 | `aarch64` | ≥ 2 GB | 🛠️ Planned | v3d mesa driver, `nixos-hardware` modules. Intended first Pi. |
| Rockchip RK3566 / RK3588 boards (Rock 5, Orange Pi 5, Quartz64) | `aarch64` | ≥ 4 GB | 🧪 Candidate | Panfrost GPU, strong CPUs. Great fit; needs bring-up. |
| Pi 3B+ and other 1 GB aarch64 boards | `aarch64` | 1 GB | ⚠️ Marginal | Boots, but Chromium is slow and tight. Simple dashboards only. |
| 32-bit ARM (Pi 2, armv7 MPU boards, STM32MP157x-DK2) | `armv7` | ≤ 512 MB | ❌ Won't work | Too little RAM + weak/GLES2-only GPU + shaky armv7 Chromium. |
| ARMv6 (Pi 1, Pi Zero / Zero W) | `armv6` | ≤ 512 MB | ❌ Won't work | No nixpkgs binary cache for armv6 — everything would build from source. |

### Good aarch64 boards to extend to

Pick **64-bit, ≥ 2 GB (prefer 4 GB+)**, with a **mainline mesa GPU driver** and a
[`nixos-hardware`](https://github.com/NixOS/nixos-hardware) module to ease bring-up:

- **Raspberry Pi 4 / 5 / 400** — best community + `nixos-hardware` support; v3d GPU. Start here.
- **Radxa Rock 5B / Rock 4** (RK3588 / RK3399) — powerful, Panfrost GPU, up to 16–32 GB RAM.
- **Orange Pi 5 / 5 Plus** (RK3588) — same SoC family, inexpensive, lots of RAM options.
- **Pine64 Quartz64 / RockPro64** (RK3566 / RK3399) — open-friendly, Panfrost.
- **Odroid N2+ / C4** (Amlogic Mali via Panfrost/Lima) — solid mainline support.
- **Libre Computer** aarch64 boards — mainline-focused.
- Reused **aarch64 tablets / Chromebooks** — integrated screen, battery and touch, if you can boot mainline.

GPU sweet spots: **Broadcom v3d** (Pi 4/5) and **Rockchip + Panfrost** (RK3566/RK3588) —
both have solid mesa Wayland drivers. Boards with only a GLES2 Vivante / older-Mali
GPU (or no mainline driver) fall back to software rendering — avoid.

> Adding an aarch64 target also means a second system closure in the build/update
> pipeline, so the update manifest is keyed by `system` (`x86_64-linux` /
> `aarch64-linux`) and each device pulls the closure matching its own arch.

## TODO

- [ ] Touch Keyboard
- [ ] Add Volume Control
- [x] Add Manage Zoom
- [x] Add Dark and Light Mode
- [ ] Support Raspberry Pi

## Inspiration

- TouchKio
- FullPageOS