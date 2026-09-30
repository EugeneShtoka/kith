# The kith package, built from source with buildGoModule. Used by ../../flake.nix;
# written as a plain callPackage function so it could move into nixpkgs unchanged.
{
  lib,
  stdenv,
  buildGoModule,
  coreutils,
  src,
  version,
  rev ? "unknown",
}:

buildGoModule {
  pname = "kith";
  inherit version src;

  # The NAR hash of `go mod vendor` for go.mod/go.sum. It changes whenever they do:
  # after a dependency bump, build once, and nix prints the right value as `got:`.
  # See docs/packaging.md, "Nix".
  vendorHash = "sha256-qlkJGn+Q7Foo9URx1gI5vJ9nSvItYXZGk8mpLKbZHWc=";

  subPackages = [
    "cmd/kith"
    "cmd/kithd"
    "cmd/kith-mcp"
  ];

  # goolm selects mautrix's pure-Go E2EE; without it the build needs libolm and cgo.
  tags = [ "goolm" ];
  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
    "-X github.com/EugeneShtoka/kith/internal/buildinfo.Version=${version}"
    "-X github.com/EugeneShtoka/kith/internal/buildinfo.Commit=${rev}"
  ];

  # CI runs the full suite (with -race, which needs cgo); several tests expect a
  # writable home and a session bus, which the build sandbox does not have.
  doCheck = false;

  postInstall =
    lib.optionalString stdenv.hostPlatform.isLinux ''
      # The same ExecStart= rewrite as `make install` and the distribution packages,
      # pointed at the store. The unsandboxed ExecStartPre= names mkdir by bare name,
      # which a regular distribution resolves in /usr/bin; NixOS has no /usr/bin.
      bash scripts/render-units.sh "$out/bin" "$out/share/systemd/user"
      substituteInPlace "$out/share/systemd/user/kithd.service" "$out/share/systemd/user/kithd@.service" \
        --replace-fail "ExecStartPre=+mkdir " "ExecStartPre=+${coreutils}/bin/mkdir "
      install -Dm644 packaging/desktop/kith.desktop "$out/share/applications/kith.desktop"
      substituteInPlace "$out/share/applications/kith.desktop" \
        --replace-fail "Exec=kith " "Exec=$out/bin/kith "
    ''
    + lib.optionalString stdenv.hostPlatform.isDarwin ''
      install -Dm644 packaging/macos/io.github.eugeneshtoka.kithd.plist -t "$out/share/kith/"
    '';

  meta = {
    description = "Terminal Matrix client with a sync daemon, pure-Go E2EE and an MCP server";
    homepage = "https://github.com/EugeneShtoka/kith";
    license = lib.licenses.mit;
    mainProgram = "kith";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}
