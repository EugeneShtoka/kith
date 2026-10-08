# NixOS module: installs kith system-wide and, optionally, starts its user daemon
# in every user session. Import it from the flake: inputs.kith.nixosModules.default
#
#   programs.kith.enable = true;
#   programs.kith.daemon.enable = true;        # kithd for every user session
#   programs.kith.profiles = [ "work" ];       # ...or kithd@<name> instead
#
# The daemon is off by default on purpose: a user unit enabled system-wide starts in
# *every* user's session, and a user who has never run `kith login` gets a daemon
# that exits for want of a session and is restarted every five seconds, forever
# (the unit never gives up — a homeserver outage must not stop it). Enable it on a
# machine whose users all use kith, or use the home-manager module instead.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.kith;
  dropin = {
    wantedBy = [ "default.target" ];
    # Keep the packaged unit and add only the [Install] wiring on top of it.
    overrideStrategy = "asDropin";
  };
in
{
  options.programs.kith = {
    enable = lib.mkEnableOption "kith, a terminal chat client, and its systemd user units";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "kith.packages.\${system}.default";
      description = "The kith package to install.";
    };
    daemon.enable = lib.mkEnableOption "the kithd user daemon in every user session";
    profiles = lib.mkOption {
      type = lib.types.listOf (lib.types.strMatching "[A-Za-z0-9._-]+");
      default = [ ];
      example = [ "work" ];
      description = "Start kithd@<name> for each profile instead of the plain kithd.";
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ];
    systemd.packages = [ cfg.package ];
    systemd.user.services = lib.mkIf cfg.daemon.enable (
      if cfg.profiles == [ ] then
        { kithd = dropin; }
      else
        lib.listToAttrs (map (p: lib.nameValuePair "kithd@${p}" dropin) cfg.profiles)
    );
  };
}
