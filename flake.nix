{
  description = "Dashboard Assistant OS — declarative single-purpose Home Assistant kiosk";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

    # Wired for the future on-disk install path (tmpfs root + ext4 /persist).
    # Not heavily used yet: the live ISO already provides an ephemeral root.
    impermanence.url = "github:nix-community/impermanence";

    # Declarative btrfs+zstd disk layout + image builder for the on-disk target.
    disko.url = "github:nix-community/disko";
    disko.inputs.nixpkgs.follows = "nixpkgs";

    # Board profiles (firmware, GPU, kernel bits) for the Raspberry Pi target.
    nixos-hardware.url = "github:NixOS/nixos-hardware";
  };

  outputs =
    {
      self,
      nixpkgs,
      impermanence,
      disko,
      nixos-hardware,
    }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      # Release version baked into every image. The daemon reports this to Home
      # Assistant as the "installed" version and compares it against the newest
      # GitHub release tag to advertise updates. Bump it in lockstep with the
      # release tag you cut (tags may carry a leading "v"; the daemon strips it).
      version = "0.2.0";

      # Optional per-build overrides (e.g. a seeded HA URL / debug flags). See
      # modules/local.example.nix. Must be git-tracked to be picked up.
      localModules = lib.optional (builtins.pathExists ./modules/local.nix) ./modules/local.nix;
    in
    {
      nixosConfigurations = {
        # Live ISO — boots from removable media (USB / SATA-via-USB adapter).
        dashboard-assistant-x86 = lib.nixosSystem {
          inherit system;
          specialArgs = { inherit impermanence version; };
          modules = [
            ./modules/hardware/generic-x86.nix
            ./modules/core/default.nix
          ]
          ++ localModules;
        };

        # Installed system — persistent, boots from a fixed SATA disk, updatable
        # with `nixos-rebuild switch`. Build a flashable image via `.#disk-image`.
        dashboard-assistant-x86-disk = lib.nixosSystem {
          inherit system;
          specialArgs = { inherit impermanence version; };
          modules = [
            disko.nixosModules.disko
            ./modules/hardware/generic-x86-disk.nix
            ./modules/core/default.nix
          ]
          ++ localModules;
        };

        # Raspberry Pi 4 (aarch64) — SD-card image, for bring-up/testing on a Pi.
        # Build the flashable image via `.#rpi4-image` (aarch64; this host builds
        # it via binfmt emulation, fetching most from the binary cache).
        dashboard-assistant-rpi4 = lib.nixosSystem {
          system = "aarch64-linux";
          specialArgs = { inherit impermanence version; };
          modules = [
            nixos-hardware.nixosModules.raspberry-pi-4
            ./modules/hardware/rpi4.nix
            ./modules/core/default.nix
          ]
          ++ localModules;
        };
      };

      # Raw btrfs+zstd EFI disk image built by disko: `nix build .#disk-image`
      # (or `just build-disk`), then dd result/dashboard-assistant.raw to the SSD. The
      # layout lives in modules/hardware/disk-layout.nix.
      packages.${system} = {
        disk-image = self.nixosConfigurations.dashboard-assistant-x86-disk.config.system.build.diskoImages;

        # vboard (on-screen keyboard) is packaged from source — not in nixpkgs.
        # Exposed here so it can be built/tested standalone (`nix build .#vboard`);
        # the kiosk module pulls it in via callPackage.
        vboard = pkgs.callPackage ./packages/vboard.nix { };
      };

      # Raspberry Pi 4 SD-card image: `nix build .#rpi4-image`, then flash
      # result/sd-image/*.img.zst to the card (zstdcat | dd, or unzstd first).
      packages.aarch64-linux.rpi4-image =
        self.nixosConfigurations.dashboard-assistant-rpi4.config.system.build.sdImage;

      devShells.${system}.default = pkgs.mkShell {
        packages = [
          pkgs.go
          pkgs.gopls
          pkgs.nixfmt
          # For `just inject-token` (CDP over the :9222 tunnel).
          pkgs.curl
          pkgs.jq
          pkgs.websocat
          # Static site generator for the docs/ site (`zensical serve`/`build`).
          pkgs.zensical
        ];
      };

      formatter.${system} = pkgs.nixfmt;
    };
}
