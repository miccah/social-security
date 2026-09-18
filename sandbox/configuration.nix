# NixOS sandbox VM: a headless guest that hosts the shared pairing tmux session.
# Clients reach it only through the host loopback forward the VM manager sets up,
# so the firewall stays off and login is key-only.
{ pkgs, modulesPath, ... }:

{
  imports = [
    # Provides config.system.build.vm, a script that boots this configuration
    # under QEMU with the host Nix store shared over 9p.
    "${modulesPath}/virtualisation/qemu-vm.nix"
  ];

  networking.hostName = "sandbox";

  # Access is confined to the host loopback forward, so nothing inside needs
  # limiting; the firewall would only obstruct the "no network limits" goal.
  networking.firewall.enable = false;

  # Headless: the manager talks to the guest over ssh, never a display.
  virtualisation.graphics = false;
  virtualisation.memorySize = 2048;
  virtualisation.cores = 2;

  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
    };
  };

  # The manager passes the session's public key through a read-only 9p share
  # (mount tag "sssh") it adds to the QEMU command at boot. nofail keeps the
  # guest bootable when no share is attached.
  fileSystems."/mnt/sssh" = {
    device = "sssh";
    fsType = "9p";
    options = [ "trans=virtio" "version=9p2000.L" "ro" "nofail" "x-systemd.device-timeout=5s" ];
  };

  # Install the session key as root's authorized key before sshd starts.
  systemd.services.sssh-authorized-keys = {
    description = "Install the session SSH key";
    before = [ "sshd.service" ];
    wantedBy = [ "multi-user.target" ];
    unitConfig = {
      RequiresMountsFor = "/mnt/sssh";
      ConditionPathExists = "/mnt/sssh/authorized_keys";
    };
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      install -d -m 700 /root/.ssh
      install -m 600 /mnt/sssh/authorized_keys /root/.ssh/authorized_keys
    '';
  };

  # The shared read/write session every client attaches to. Started detached so
  # the tmux server outlives this oneshot and `tmux attach -t pairing` finds it.
  systemd.services.pairing = {
    description = "Shared tmux pairing session";
    after = [ "network.target" ];
    wantedBy = [ "multi-user.target" ];
    environment.HOME = "/root";
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${pkgs.tmux}/bin/tmux new-session -d -s pairing";
      ExecStop = "${pkgs.tmux}/bin/tmux kill-server";
    };
  };

  environment.systemPackages = with pkgs; [ tmux git vim curl ];

  # First release this configuration targets; pins stateful defaults.
  system.stateVersion = "26.05";
}
