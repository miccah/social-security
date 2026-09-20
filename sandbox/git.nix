# Git layer: makes the mounted project committable inside the session while
# disabling push for everyone.
#
# Commits are authored by the owner, whose identity the manager stages from the
# host, and co-authored by the connected users. The sssh commit helper, also
# staged by the manager, runs as the prepare-commit-msg and commit-msg hooks: it
# generates the Co-authored-by trailers from the live connected set and learns
# each user's email from their first commit. Push is disabled: no push
# credentials are placed in the VM, and a pre-push hook rejects every push.
{ pkgs, projectDir, ... }:

let
  # Runtime dir on tmpfs holding the staged helper, the owner identity include,
  # and the username-to-email store the helper maintains.
  runtimeDir = "/run/sssh";
  helper = "${runtimeDir}/sssh";
  identity = "${runtimeDir}/identity";

  # Hooks route git through the helper. prepare/record no-op when the helper is
  # absent so commits still work; pre-push always rejects.
  prepareHook = pkgs.writeShellScript "prepare-commit-msg" ''
    [ -x ${helper} ] && exec ${helper} commit prepare "$1"
    exit 0
  '';
  recordHook = pkgs.writeShellScript "commit-msg" ''
    [ -x ${helper} ] && exec ${helper} commit record "$1"
    exit 0
  '';
  prePushHook = pkgs.writeShellScript "pre-push" ''
    echo "sssh: git push is disabled for this pairing session" >&2
    exit 1
  '';
  hooks = pkgs.linkFarm "sssh-git-hooks" [
    { name = "prepare-commit-msg"; path = prepareHook; }
    { name = "commit-msg"; path = recordHook; }
    { name = "pre-push"; path = prePushHook; }
  ];
in {
  # System git config: route hooks through the helper, trust the 9p-mounted
  # project despite its host ownership, and pull the owner identity from the
  # runtime include the setup service writes.
  environment.etc."gitconfig".text = ''
    [core]
        hooksPath = ${hooks}
    [safe]
        directory = ${projectDir}
    [include]
        path = ${identity}
  '';

  # Install the staged helper and owner identity before anything commits. The
  # manager places the sssh binary and optional git.name/git.email in /tmp/shared
  # before boot; this puts them where the git config and hooks expect them.
  systemd.services.sssh-git-setup = {
    description = "Install the sssh git helper and owner identity";
    wantedBy = [ "multi-user.target" ];
    before = [ "pairing.service" ];
    unitConfig.RequiresMountsFor = "/tmp/shared";
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      install -d -m 755 ${runtimeDir}
      if [ -e /tmp/shared/sssh ]; then
        install -m 755 /tmp/shared/sssh ${helper}
      fi
      {
        echo '[user]'
        [ -e /tmp/shared/git.name ]  && printf '\tname = %s\n'  "$(cat /tmp/shared/git.name)"
        [ -e /tmp/shared/git.email ] && printf '\temail = %s\n' "$(cat /tmp/shared/git.email)"
      } > ${identity}
    '';
  };
}
