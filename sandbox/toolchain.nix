# Project-toolchain layer: the host project directory is mounted into the guest
# read/write, and every pairing pane enters its flake dev environment via `nix
# develop`. Edits are live on the host. A project with no flake falls back to the
# owner's login shell. The host Nix store is shared by qemu-vm.nix, so inputs the
# owner has already built resolve without a rebuild.
{ pkgs, projectDir, ... }:

let
  # The command every pairing pane runs. It enters the project's dev shell when
  # the project has a flake, and otherwise starts the owner's login shell. Kept
  # in tmux's default-command so it applies to the first pane and to any window
  # or pane opened later. A failed `nix develop` falls back to the login shell
  # rather than leaving a dead pane.
  paneCommand = pkgs.writeShellScript "sssh-pane" ''
    shell="$(${pkgs.getent}/bin/getent passwd "$(id -un)" | cut -d: -f7)"
    [ -n "$shell" ] || shell=/bin/sh
    cd ${projectDir} 2>/dev/null || true
    if [ -e ${projectDir}/flake.nix ]; then
      nix develop ${projectDir} --command "$shell" -l && exit 0
    fi
    exec "$shell" -l
  '';
in {
  # direnv + nix-direnv + the zsh hook (programs.zsh is enabled in owner-env), so
  # a project `.envrc` (`use flake`) auto-loads on cd and nix-direnv caches the
  # dev shell for instant re-entry. The pane command's login-shell fallback picks
  # this up without further wiring.
  programs.direnv.enable = true;

  # The manager exports the project over 9p read/write with mount tag "project".
  # qemu-vm.nix overrides top-level fileSystems with mkVMOverride, so guest mounts
  # must be declared here under virtualisation.fileSystems. nofail keeps the guest
  # bootable when no project is attached; the pane command then falls back to the
  # login shell.
  virtualisation.fileSystems.${projectDir} = {
    device = "project";
    fsType = "9p";
    options = [ "trans=virtio" "version=9p2000.L" "nofail" "x-systemd.device-timeout=5s" ];
  };

  # The shared read/write session every client attaches to. Started detached so
  # the tmux server outlives this oneshot and `tmux attach -t pairing` finds it.
  systemd.services.pairing = {
    description = "Shared tmux pairing session";
    after = [ "network.target" ];
    wantedBy = [ "multi-user.target" ];
    environment.HOME = "/root";
    # The tmux server inherits this PATH, and run-shell executes the plugin
    # scripts (sensible, pain-control) in the server environment: they start
    # with `#!/usr/bin/env bash` and call bare `tmux`/`grep`, so without these
    # each plugin exits 127.
    path = with pkgs; [ tmux bashInteractive gnugrep coreutils ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "sssh-pairing-start" ''
        ${pkgs.tmux}/bin/tmux set-option -g default-command ${paneCommand}
        ${pkgs.tmux}/bin/tmux new-session -d -s pairing -c ${projectDir}
      '';
      ExecStop = "${pkgs.tmux}/bin/tmux kill-server";
    };
  };
}
