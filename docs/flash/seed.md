# Seed File

A **seed file** provisions a freshly flashed device without touching the
on-screen setup wizard. Drop a `dashboard-assistant.yaml` next to the image and the
device picks up its Home Assistant URL, access token and Wi-Fi on first boot —
handy for field deploys or flashing several tablets at once.

!!! warning "Physical access = full trust"
    Any USB stick carrying a `dashboard-assistant.yaml` is applied automatically, and
    the file holds a long-lived token in plain text. Only use seed files on
    hardware and networks you control, and wipe the stick afterwards.

## Populate Seed File

Create a file named exactly `dashboard-assistant.yaml`. Every key is optional — include
only what you want to provision:

```yaml
# Home Assistant base URL the kiosk should open.
ha_url: "https://homeassistant.local:8123"

# Long-lived access token for the dedicated kiosk user. See
# "Create Home Assistant User" for how to generate one.
token: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiIyZWRlNGE0ZTFjNmQ0ZDY3OTY4ODhmMTk5OGNhNWVjMSIsImlhdCI6MTc4NDcxODk3MywiZXhwIjoyMTAwMDc4OTczfQ.Rd92pdzdYkC8HI3buVO6m9EVVI71Ye-MP_1nwogfOgU"

# Optional Wi-Fi credentials. Omit the whole block on a wired device.
wifi:
  ssid: "MyNetwork"
  psk: "supersecret"
```

| Key         | Required | Description                                                      |
| ----------- | -------- | ---------------------------------------------------------------- |
| `ha_url`    | no       | Home Assistant base URL the dashboard loads on boot.             |
| `token`     | no       | Long-lived access token, stored and injected to auto-login.      |
| `wifi.ssid` | no       | Wi-Fi network name to join.                                      |
| `wifi.psk`  | no       | Wi-Fi pre-shared key (password).                                 |

## Apply the Seed File

There are two ways to hand the file to a device. Both require the image to be
built with config import enabled (`dashboard.configImport.enable = true`).

=== "`ESP / boot partition`"

    The `/boot` partition is FAT and readable on any computer. After flashing,
    mount it and copy `dashboard-assistant.yaml` to its root.

    It is imported once on first boot, while the device is still
    unprovisioned — so it seeds a fresh image without re-importing on every
    later boot.

=== "`USB stick`"

    Put `dashboard-assistant.yaml` in the root of a normal USB stick (any filesystem
    the OS can mount) and plug it into a running device.

    Inserting the stick triggers an import immediately, so you can re-provision
    a device in the field without reflashing.

Once imported, the setup wizard is skipped and the dashboard goes straight to
your Home Assistant instance.
