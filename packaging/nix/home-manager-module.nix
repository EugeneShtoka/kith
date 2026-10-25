# home-manager module: kith for one user, with its daemon started in that user's
# session. Import it from the flake: inputs.kith.homeManagerModules.default
#
#   programs.kith.enable = true;
#
# Linux: links the packaged unit into ~/.config/systemd/user and enables it the
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
      description = "Start kithd in this user's session (systemd on Linux, launchd on macOS).";
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      { home.packages = [ pkg ]; }
      (lib.mkIf isLinux {
        xdg.configFile = {
          "systemd/user/kithd.service".source = "${units}/kithd.service";
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
      })
    ]
  );
}
