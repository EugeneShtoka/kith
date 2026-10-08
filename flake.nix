{
  description = "kith: terminal chat client for Matrix, WhatsApp, Telegram and Slack, with a sync daemon, pure-Go E2EE and an MCP server";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      date = builtins.substring 0 8 (self.lastModifiedDate or "19700101");
    in
    {
      packages = forAllSystems (pkgs: rec {
        kith = pkgs.callPackage ./packaging/nix/package.nix {
          src = self;
          version = "0-unstable-${date}";
          rev = self.shortRev or self.dirtyShortRev or "unknown";
        };
        default = kith;
      });

      nixosModules.default = import ./packaging/nix/nixos-module.nix self;
      homeManagerModules.default = import ./packaging/nix/home-manager-module.nix self;

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go ];
          env.GOFLAGS = "-tags=goolm";
        };
      });
    };
}
