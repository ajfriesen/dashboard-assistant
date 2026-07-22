# Flash Image

If you have access to the storage medium, you can just flash the image.

## Requirents

Depending on your hardware you would need:

- SD Card Reader
- Sata reader
- NVME Reader

## Download the image

??? note "Click to reveal"
    This content is hidden by default. 
    
    You can include multiple paragraphs, lists, or other Markdown inside, as long as it is indented by 4 spaces.


## Flashing the image

=== "`Linux`"

    ```
    lsblk -o NAME,SIZE,MODEL,TRAN
    ```

    ```bash
    xz -dc nixos-*.iso.xz | sudo dd of=/dev/sdX bs=4M conv=fsync oflag=direct status=progress
    sync
    ```

=== "`MacOS`"

    ```
    lsblk -o NAME,SIZE,MODEL,TRAN
    ```

    ```bash
    xz -dc nixos-*.iso.xz | sudo dd of=/dev/sdX bs=4M conv=fsync oflag=direct status=progress
    sync
    ```

=== "`Windows/GUI`"

    1. Downlaod [balenaEtcher](https://etcher.balena.io/)
    2. Flash

