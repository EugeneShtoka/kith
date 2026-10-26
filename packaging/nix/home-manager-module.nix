# home-manager module: kith for one user, with its daemon and kith-notify (its desktop
# notifications) started in that user's session. Import it from the flake:
# inputs.kith.homeManagerModules.default
#
#   programs.kith.enable = true;
#
# Linux: links the packaged units into ~/.config/systemd/user and enables them the
# way `systemctl --user enable` would (a symlink in default.target.wants). Run
# `systemctl --user daemon-reload` after the first switch, then `kith login`.
# macOS: a launchd agent, the counterpart of packaging/macos/.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.kith;
  pkg = cfg.package;
  units = "${pkg}/share/systemd/user";
  inherit (pkgs.stdenv.hostPlatform) isLinux isDarwin;
  wants = {
    "systemd/user/default.target.wants/kithd.service".source = "${units}/kithd.service";
    "systemd/user/default.target.wants/kith-notify.service".source = "${units}/kith-notify.service";
  };
in
{
  options.programs.kith = {
    enable = lib.mkEnableOption "kith, a terminal chat client";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "kith.packages.\${system}.default";
      description = "The kith package to install.";
    };
    daemon.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Start kithd and kith-notify in this user's session (systemd on Linux, launchd on macOS).";
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      { home.packages = [ pkg ]; }
      (lib.mkIf isLinux {
        xdg.configFile = {
          "systemd/user/kithd.service".source = "${units}/kithd.service";
          "systemd/user/kith-notify.service".source = "${units}/kith-notify.service";
        }
        // lib.optionalAttrs cfg.daemon.enable wants;
      })
      (lib.mkIf (isDarwin && cfg.daemon.enable) {
        launchd.agents.kithd = {
          enable = true;
          config = {
            ProgramArguments = [ "${pkg}/bin/kithd" ];
            RunAtLoad = true;
            KeepAlive.SuccessfulExit = false;
            ThrottleInterval = 5;
            Umask = 63; # 0077
            StandardErrorPath = "${config.home.homeDirectory}/Library/Logs/kithd.log";
          };
        };
        launchd.agents.kith-notify = {
          enable = true;
          config = {
            ProgramArguments = [ "${pkg}/bin/kith-notify" ];
            RunAtLoad = true;
            KeepAlive.SuccessfulExit = false;
            ThrottleInterval = 5;
            StandardErrorPath = "${config.home.homeDirectory}/Library/Logs/kith-notify.log";
          };
        };
      })
    ]
  );
}
