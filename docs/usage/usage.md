# Usage

Once provisioned, the device boots straight into your Home Assistant dashboard
and stays there. Day-to-day you control it in two places: **on the screen
itself** and **from Home Assistant** over MQTT.

## On the device

A small bar gives you touch controls without leaving the kiosk:

- **Next / Previous page** — cycle through your configured dashboard URLs.
- **⌨ Keyboard** — toggle the on-screen keyboard for text fields on touch-only
  devices *(experimental)*.
- **Touch to wake** — tapping the screen wakes the display when it has slept.

## From Home Assistant

Give the device your MQTT broker and it auto-discovers as a single **Dashboard
Assistant** device. From there you can:

- Turn the **display** on/off and set **brightness**.
- Adjust **zoom** and flip **dark mode**.
- Take a **screenshot** of the current view on demand.
- Switch the active **page**, or edit the list of dashboard URLs.
- **Reboot** or **shut down** the device.
- Install an **OS update** when one is available.

It also reports sensors — CPU, memory, storage, temperature, uptime, battery
(when present) and idle time — so you can build automations (for example, dim
the panel at night or wake it on motion).

See the [Home Assistant integration](../about/features.md#home-assistant-integration)
reference for the complete entity list.

## Updates

OS updates are atomic and surface in Home Assistant as an `update` entity: it
compares the installed version against the latest release and offers a one-tap
install. If a new generation fails to boot cleanly, the device automatically
rolls back to the previous working one.

## Recovery

If a boot goes wrong, the device falls back to the last known-good generation on
its own. For manual recovery, an on-screen picker lets you choose an older
generation to boot into — useful if a configuration change misbehaves but the
system still boots.

!!! warning "Automatic rollback needs systemd-boot"
    The *automatic* reboot-into-the-previous-generation on a failed boot relies
    on **systemd-boot** and its boot-counting support. It does **not** work with
    **U-Boot**, which the Raspberry Pi images use — on those boards you'd recover
    manually with the picker instead. (This is my last understanding; it may
    change as the Pi targets mature.)
