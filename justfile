build-image:
  nix build .#nixosConfigurations.dashboard-x86.config.system.build.isoImage

# Build the installable raw disk image (btrfs+zstd, built by disko). dd
# result/ha-dashboard.raw to the SSD, then boot it from the native SATA port.
build-disk:
  nix build .#disk-image
  @echo
  @echo "Image: $(readlink -f result)/ha-dashboard.raw"
  @echo "Flash it (confirm the device first!):"
  @echo "  sudo dd if=$(readlink -f result)/ha-dashboard.raw of=/dev/sdX bs=4M oflag=sync conv=fsync status=progress"

# Boot the built ISO. The virtio-net NIC gets DHCP from QEMU's user-mode
# network, so NetworkManager auto-connects it — first boot lands in the setup
# wizard showing "Connected via ethernet" (the wired / existing-connection path).
# Interact with the wizard directly on the QEMU display, or drive it from the
# host via `just qemu-ssh` (see the loopback note there).
qemu-run:
  qemu-system-x86_64 \
    -enable-kvm \
    -m 2048 -smp 2 \
    -machine q35 \
    -cdrom result/iso/*.iso \
    -device virtio-vga-gl \
    -display gtk,gl=on \
    -netdev user,id=net0,hostfwd=tcp::2222-:22 \
    -device virtio-net,netdev=net0 \
    -device virtio-serial-pci \
    -chardev qemu-vdagent,id=vdagent,name=vdagent,clipboard=on \
    -device virtserialport,chardev=vdagent,name=com.redhat.spice.0

# SSH into the running VM with the daemon tunnelled to the host. The -L tunnel
# delivers to the guest's *loopback*, which satisfies the wizard's loopback-only
# guard — so while this session is open you can open http://localhost:8080/setup
# in a host browser, or curl the provisioning API. (A raw QEMU port-forward would
# arrive as non-loopback and get 403.) Requires SSH login to be enabled on the
# image — see the note in README/daemon for the test-only credentials snippet.
qemu-ssh:
  ssh -p 2222 \
    -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR \
    -L 8080:localhost:8080 -L 9222:localhost:9222 root@localhost -vvv

# Exercise the networking API from inside the guest (loopback, so guarded
# endpoints work). Runs over SSH without needing the tunnel session open.
net-check:
  ssh -p 2222 -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR \
    root@localhost 'curl -fsS localhost:8080/api/state; echo; \
    curl -fsS localhost:8080/api/netinfo; echo'
# Log the kiosk into Home Assistant by injecting a long-lived access token
# straight into the page via CDP — no Chrome, no chrome://inspect, no version
# mismatch. Requires `just qemu-ssh` running in another terminal (it tunnels
# :9222) and dashboard.debug.chromiumRemoteDebugging = true baked into the image.
# Create the token in HA: Profile -> Security -> Long-lived access tokens.
#   just inject-token "eyJhbGciOi..."
inject-token token:
  #!/usr/bin/env bash
  set -euo pipefail
  ws=$(curl -fsS http://localhost:9222/json \
        | jq -r '[.[] | select(.type=="page")][0].webSocketDebuggerUrl // empty')
  if [ -z "$ws" ]; then
    echo "No inspectable page on :9222 — is 'just qemu-ssh' running and remote debugging on?" >&2
    exit 1
  fi
  # Navigate to the app root, not reload(): hassTokens is consumed by the main
  # app entrypoint, while /auth/authorize (the login screen) ignores it.
  expr='localStorage.setItem("hassTokens", JSON.stringify({access_token:"{{token}}",token_type:"Bearer",expires_in:315360000,expires:Date.now()+315360000000,refresh_token:"",clientId:null,hassUrl:location.origin})); location.replace(location.origin + "/");'
  jq -cn --arg e "$expr" '{id:1,method:"Runtime.evaluate",params:{expression:$e}}' \
    | timeout 5 websocat "$ws" || true
  echo "Token injected — the kiosk should load the dashboard logged in."

# Evaluate arbitrary JS in the kiosk page over CDP and print the raw result.
# Needs `just qemu-ssh` running. Use double quotes inside the expression.
#   just cdp-eval 'location.href'
#   just cdp-eval 'localStorage.getItem("hassTokens")'
cdp-eval expr:
  #!/usr/bin/env bash
  set -euo pipefail
  ws=$(curl -fsS http://localhost:9222/json \
        | jq -r '[.[] | select(.type=="page")][0].webSocketDebuggerUrl // empty')
  if [ -z "$ws" ]; then echo "No inspectable page on :9222" >&2; exit 1; fi
  jq -cn --arg e '{{expr}}' \
    '{id:1,method:"Runtime.evaluate",params:{expression:$e,returnByValue:true}}' \
    | timeout 5 websocat "$ws" || true
