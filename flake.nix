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

    # The owner's running host configuration, evaluated the same way
    # nixos-rebuild does. The sandbox borrows the owner's editor, shell, and
    # tmux from here so the shared session matches their usual setup. This reads
    # <nixpkgs> and /etc/nixos, so the sandbox must be built with `nix build
    # --impure`; the result is host-coupled and not reproducible, which is
    # acceptable because the sandbox VM is ephemeral. Evaluated lazily, so the
    # other outputs stay pure.
    hostConfig = (import <nixpkgs/nixos> {
      inherit system;
      configuration = /etc/nixos/configuration.nix;
    }).config;
  in {
    devShells.${system}.default = pkgs.mkShell {
      packages = with pkgs; [
        # Go toolchain for cmd/sssh and internal/*.
        go
        gopls
      ];
    };

    # The sandbox VM the manager boots. The base module comes from this flake;
    # the owner-environment layer is borrowed impurely from the host (hostConfig)
    # and the project toolchain is entered at session start, so the flake
    # reference stays independent of the project.
    nixosConfigurations.sandbox = nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        ./sandbox/configuration.nix
        { _module.args.hostConfig = hostConfig; }
      ];
    };
  };
}
