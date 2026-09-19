# NixOS sandbox VM: a headless guest that hosts the shared pairing tmux session.
# Clients reach it only through the host loopback forward the VM manager sets up,
# so the firewall stays off and login is key-only.
#
# The VM is composed of three layers: this base module, the owner-environment
# module (the owner's editor/shell/tmux, borrowed from the host), and the
# toolchain module (the project's flake dev environment, entered at session
# start). The base is applied last so its security settings win where they
# collide.
{ pkgs, modulesPath, ... }:

{
  imports = [
    # Provides config.system.build.vm, a script that boots this configuration
    # under QEMU with the host Nix store shared over 9p.
    "${modulesPath}/virtualisation/qemu-vm.nix"
    ./owner-env.nix
    ./toolchain.nix
    ./git.nix
  ];

  networking.hostName = "sandbox";

  # Access is confined to the host loopback forward, so nothing inside needs
  # limiting; the firewall would only obstruct the "no network limits" goal.
  networking.firewall.enable = false;

  # Headless: the manager talks to the guest over ssh, never a display.
  virtualisation.graphics = false;
  virtualisation.memorySize = 2048;
  virtualisation.cores = 2;

  # Flakes power the project toolchain (`nix develop`). qemu-vm.nix shares the
  # host Nix store into the guest, so inputs the owner has already built resolve
  # without a rebuild.
  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
    };
  };

  # The manager stages the session's public key in the host directory the run
  # script shares into the guest at /tmp/shared (via SHARED_DIR). This installs
  # it as root's authorized key, owned and permissioned for sshd, before sshd
  # starts.
  systemd.services.sssh-authorized-keys = {
    description = "Install the session SSH key";
    before = [ "sshd.service" ];
    wantedBy = [ "multi-user.target" ];
    unitConfig = {
      RequiresMountsFor = "/tmp/shared";
      ConditionPathExists = "/tmp/shared/authorized_keys";
    };
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      install -d -m 700 /root/.ssh
      install -m 600 /tmp/shared/authorized_keys /root/.ssh/authorized_keys
    '';
  };

  # Base tools present regardless of the owner environment or project toolchain.
  environment.systemPackages = with pkgs; [ git curl ];

  # First release this configuration targets; pins stateful defaults.
  system.stateVersion = "26.05";
}
