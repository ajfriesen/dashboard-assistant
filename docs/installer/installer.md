---
icon: material/usb-flash-drive
---

# First-Boot Setup

After you [flash the image](../flash/flash.md) and boot the device for the first
time, it comes up **unprovisioned** and shows an on-screen setup wizard instead
of a dashboard. Everything happens on the device's own screen — no separate
computer needed.

## The setup wizard

The wizard walks you through the two things the kiosk needs to reach your
dashboard:

1. **Network** — join a Wi-Fi network (scan, pick, enter the password), or skip
   it if the device is on wired Ethernet.
2. **Home Assistant URL** — the address the kiosk should open, e.g.
   `https://homeassistant.local:8123`.

Provide a Home Assistant access token here too (or later) so the kiosk logs in
automatically instead of stopping at the login screen. See
[Create Home Assistant User](../flash/home-assistant-setup.md) for how to make a
dedicated kiosk user and generate that token.

Once you finish, the device stores the settings, skips the wizard on subsequent
boots, and opens straight into your dashboard.

## Skipping the wizard (headless provisioning)

If you're setting up several devices, or want a fully hands-off first boot, you
can drop a [seed file](../flash/seed.md) on the boot partition or a USB stick.
The device applies it automatically and goes straight to the dashboard without
ever showing the wizard.

## MQTT integration

To have the panel appear back inside Home Assistant as a device with its own
controls and sensors, point it at your MQTT broker. See the
[Home Assistant integration](../about/features.md#home-assistant-integration)
reference for the full list of entities it exposes.
