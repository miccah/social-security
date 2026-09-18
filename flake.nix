{
  description = "ss";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  };

  outputs = { self, nixpkgs }:
  let
    system = "x86_64-linux";
    pkgs = import nixpkgs {
      inherit system;
    };
  in {
    devShells.${system}.default = pkgs.mkShell {
      packages = with pkgs; [
        # Go toolchain for cmd/sssh and internal/*.
        go
        gopls
      ];
    };

    # The sandbox VM the manager boots. Built from this flake, never from the
    # project directory, so any project boots the identical sandbox.
    nixosConfigurations.sandbox = nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [ ./sandbox/configuration.nix ];
    };
  };
}
