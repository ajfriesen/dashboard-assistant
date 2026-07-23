# Hardware Support

Dashboard Assistant ships as a single `x86_64` disk image today. Other
architectures are in progress — the table below tracks the current state.

| Target | Status | Notes |
|---|---|---|
| x86_64 (mini-PC, laptop, tablet) | ✅ Supported | Primary, tested target. Ships as a flashable disk image. |
| Raspberry Pi 4 (aarch64) | 🚧 Work in progress | SD-card image builds; used for bring-up and testing. |
| Raspberry Pi 5 (aarch64) | 🔭 Planned | On the roadmap — support is being worked on. |
| Other aarch64 boards | 🔭 Planned | Not yet packaged. |

## x86_64

Any machine with decent mainline Linux support should work — most mini-PCs,
old laptops and x86 tablets qualify. The image bundles broad hardware and
firmware support rather than being trimmed to one board, so the same file boots
across a wide range of devices.

Requirements:

- **UEFI** boot (the disk image ships an EFI system partition).
- A writable target disk — internal SSD/eMMC, a SATA drive, or an NVMe drive.
- A touchscreen is recommended for wall-panel use, but not required.

## Raspberry Pi

Raspberry Pi 4 support exists as a separate SD-card image and is used for
development. It is not yet a polished, release-grade target — expect rough edges.

Raspberry Pi 5 is on the roadmap and is actively being worked on. Follow the
project on [GitHub](https://github.com/ajfriesen/dashboard-assistant) for
progress on both boards.
