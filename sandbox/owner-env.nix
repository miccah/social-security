# Owner-environment layer: the sandbox borrows the owner's editor, shell, and
# tmux from their running host configuration (hostConfig, evaluated impurely in
# flake.nix). Only these are taken; the host's services, users, and secrets are
# left behind. The shared tmux session and every pane therefore match the
# owner's usual setup.
{ pkgs, lib, hostConfig, ... }:

let
  # Pull a package out of the host's systemPackages by its binary name, so the
  # scripts stay defined once in the host config. The name is the binary
  # (writeShellScriptBin's first argument), not the host's let-binding.
  fromHost = name:
    lib.findFirst (p: lib.getName p == name)
      (throw "sandbox: ${name} not in host systemPackages")
      hostConfig.environment.systemPackages;
in {
  environment.systemPackages = [
    # The owner's neovim, wrapped with their plugins and config.
    hostConfig.programs.neovim.finalPackage
    pkgs.tmux
    pkgs.zsh
    # The owner's clipboard scripts, borrowed from the host.
    (fromHost "yank")
    (fromHost "put")
  ];

  # The owner's editor is the default.
  environment.variables.EDITOR = "nvim";

  # The owner's tmux configuration drives the shared session; plugins it
  # references resolve through the shared host Nix store.
  environment.etc."tmux.conf".source = hostConfig.environment.etc."tmux.conf".source;

  # The owner's system-level zsh: a valid login shell (so the nix directories
  # stay on PATH) carrying the owner's interactive config. Only system-level
  # configuration is borrowed; the owner's personal home-manager dotfiles are
  # not reproduced here.
  users.users.root.shell = pkgs.zsh;
  programs.zsh = {
    enable = true;
    enableCompletion = hostConfig.programs.zsh.enableCompletion;
    histSize = hostConfig.programs.zsh.histSize;
    setOptions = hostConfig.programs.zsh.setOptions;
    shellAliases = hostConfig.programs.zsh.shellAliases;
    shellInit = hostConfig.programs.zsh.shellInit;
    loginShellInit = hostConfig.programs.zsh.loginShellInit;
    interactiveShellInit = hostConfig.programs.zsh.interactiveShellInit;
    promptInit = hostConfig.programs.zsh.promptInit;
  };
}
