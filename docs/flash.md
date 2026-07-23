# Flash the Image

If you can reach the target's storage medium (an internal SSD, a removable SD
card, or a drive in a USB adapter), the quickest path is to flash the disk image
directly.

## Requirements

Depending on your hardware you'll need a way to write to the target disk:

- An SD-card reader, **or**
- A SATA-to-USB adapter, **or**
- An NVMe/USB enclosure, **or**
- Direct access to the machine's internal disk.

You'll also need `zstd` (Linux/macOS) or a GUI flasher such as
[balenaEtcher](https://etcher.balena.io/) (Windows) to write the image.

## Download the image

Grab the latest x86_64 disk image from the
[GitHub releases page](https://github.com/ajfriesen/dashboard-assistant/releases/latest).
The download link sits at the top of the release notes and points to a
Cloudflare R2 bucket (GitHub can't host the multi-GB file directly).

The asset is a zstd-compressed raw disk image named like:

```
dashboard-assistant-<version>-x86_64.raw.zst
```

!!! tip "Verify the download"
    The image is several GB even compressed. If a flash fails midway, re-download
    and check the file size against the release page before retrying.

## Flash the image

!!! danger "Double-check the target device"
    `dd` writes with no confirmation. Flashing the wrong disk will **erase it**.
    List your disks first and confirm the device node before running the command.

=== "Linux"

    Identify the target disk:

    ```bash
    lsblk -o NAME,SIZE,MODEL,TRAN
    ```

    Decompress and write it in one pipe (replace `/dev/sdX` with your device):

    ```bash
    zstd -dc dashboard-assistant-*-x86_64.raw.zst \
      | sudo dd of=/dev/sdX bs=4M conv=fsync oflag=direct status=progress
    sync
    ```

=== "macOS"

    Identify the target disk:

    ```bash
    diskutil list
    ```

    Unmount it (don't eject), then decompress and write to the *raw* device node
    (`/dev/rdiskN` is faster than `/dev/diskN`):

    ```bash
    diskutil unmountDisk /dev/diskN
    zstd -dc dashboard-assistant-*-x86_64.raw.zst \
      | sudo dd of=/dev/rdiskN bs=4m
    ```

    `zstd` is available via [Homebrew](https://brew.sh/): `brew install zstd`.

=== "Windows / GUI"

    1. Download [balenaEtcher](https://etcher.balena.io/).
    2. Decompress the `.raw.zst` file with [7-Zip](https://www.7-zip.org/) (or
       `zstd -d`) to get a `.raw` image.
    3. In Etcher, choose **Flash from file**, select the `.raw` image, pick the
       target drive, and flash.

Once the flash finishes, move the disk into the target machine (or leave it in
place) and boot it. On first boot you'll land in the on-screen
[setup wizard](installer/installer.md) — or provision headlessly with a
[seed file](flash/seed.md).
