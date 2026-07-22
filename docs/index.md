---
icon: lucide/rocket
---

# Dashboard Assistant OS

A declarative, **unbreakable Home Assistant kiosk OS** built on NixOS. Flash it,
point it at your Home Assistant, and get a self-contained wall dashboard that
integrates back into HA over MQTT.

<figure markdown="span">
  ![Image title](https://dummyimage.com/600x400/){ width="300" }
  <figcaption>Image caption</figcaption>
</figure>

## Goals

- **Easy flash and install** — no Linux knowledge needed.
- **Integrates with Home Assistant via MQTT** — the device exposes itself as
  entities you can automate.
- **Unbreakable system via NixOS** — a bad update boots into the last working
  generation.
- **Broad hardware support** — x86_64 today; Raspberry Pi and other aarch64
  boards planned.

## Features

- Easy install and over-the-air updates (via Home Assistant or the local GUI).
- Configure multiple dashboard URLs and cycle between them.
- Wake the display by touch.
- Control from Home Assistant:
    - display on/off
    - brightness
    - zoom
    - dark mode
    - screenshot
    - reboot/shutdown
    - update
- Monitor battery, temperature, CPU, memory and storage.
