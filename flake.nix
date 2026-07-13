{
  description = "HA Dashboard OS — declarative single-purpose Home Assistant kiosk";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

    # Wired for the future on-disk install path (tmpfs root + ext4 /persist).
    # Not heavily used yet: the live ISO already provides an ephemeral root.
    impermanence.url = "github:nix-community/impermanence";
  };

  outputs = { self, nixpkgs, impermanence }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      # Optional per-build overrides (e.g. a seeded HA URL / debug flags). See
      # modules/local.example.nix. Must be git-tracked to be picked up.
      localModules = lib.optional (builtins.pathExists ./modules/local.nix) ./modules/local.nix;
    in
    {
      nixosConfigurations = {
        # Live ISO — boots from removable media (USB / SATA-via-USB adapter).
        dashboard-x86 = lib.nixosSystem {
          inherit system;
          specialArgs = { inherit impermanence; };
          modules = [
            ./modules/hardware/generic-x86.nix
            ./modules/core/default.nix
          ] ++ localModules;
        };

        # Installed system — persistent, boots from a fixed SATA disk, updatable
        # with `nixos-rebuild switch`. Build a flashable image via `.#disk-image`.
        dashboard-x86-disk = lib.nixosSystem {
          inherit system;
          specialArgs = { inherit impermanence; };
          modules = [
            ./modules/hardware/generic-x86-disk.nix
            ./modules/core/default.nix
          ] ++ localModules;
        };
      };

      # Raw EFI disk image: `nix build .#disk-image` (or `just build-disk`),
      # then dd result/nixos.img to the SSD.
      packages.${system}.disk-image = import "${nixpkgs}/nixos/lib/make-disk-image.nix" {
        inherit lib pkgs;
        config = self.nixosConfigurations.dashboard-x86-disk.config;
        partitionTableType = "efi";
        format = "raw";
        diskSize = 10240;
        label = "nixos";
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [
          pkgs.go
          pkgs.gopls
          pkgs.nixfmt
          # For `just inject-token` (CDP over the :9222 tunnel).
          pkgs.curl
          pkgs.jq
          pkgs.websocat
        ];
      };

      formatter.${system} = pkgs.nixfmt;
    };
}
