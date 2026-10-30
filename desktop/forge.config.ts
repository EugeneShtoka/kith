import { chmod, lstat, readdir } from "node:fs/promises";
import path from "node:path";

import { MakerDeb } from "@electron-forge/maker-deb";
import { MakerRpm } from "@electron-forge/maker-rpm";
import { MakerZIP } from "@electron-forge/maker-zip";
import { FusesPlugin } from "@electron-forge/plugin-fuses";
import { VitePlugin } from "@electron-forge/plugin-vite";
import type { ForgeConfig } from "@electron-forge/shared-types";
import { FuseV1Options, FuseVersion } from "@electron/fuses";

// What the deb and rpm say of themselves, as the kith package's (.goreleaser.yaml).
const homepage = "https://github.com/EugeneShtoka/kith";

/**
 * withoutSharedWrite takes write from group and others on everything under dir. Electron's
 * zip stores its files 0666 and 0777 and the extraction keeps those modes, so the deb and
 * rpm would install, owned by root, a binary any user may rewrite; and on Linux nothing
 * checks the ASAR's hash (that fuse is enforced on macOS and Windows only).
 */
async function withoutSharedWrite(dir: string): Promise<void> {
  for (const name of await readdir(dir, { recursive: true })) {
    const file = path.join(dir, name);
    const stat = await lstat(file);
    if (!stat.isSymbolicLink() && (stat.mode & 0o022) !== 0) {
      await chmod(file, stat.mode & 0o7755);
    }
  }
}

const config: ForgeConfig = {
  packagerConfig: {
    asar: true,
    // /usr/bin/kith is the terminal client, so the window's binary has a name of its own.
    executableName: "kith-desktop",
    // The relay the app runs (main.ts, relayFile), beside it in resources/: the one
    // `make build-relay` built from this tree, or in a release the tag's (release.yml).
    extraResource: ["../kith-relay"],
  },
  hooks: {
    packageAfterExtract: (_config, buildPath) => withoutSharedWrite(buildPath),
  },
  makers: [
    new MakerZIP({}, ["linux"]),
    // kithd comes from the kith package; the window is no use without one to reach
    // (Recommends: rpm's template has no such field, docs/desktop.md says to install both).
    new MakerDeb({
      options: {
        maintainer: "Eugene Shtoka <eugene@cloud-surf.com>",
        homepage,
        section: "net",
        categories: ["Network"],
        recommends: ["kith"],
      },
    }),
    new MakerRpm({ options: { homepage, license: "AGPL-3.0-or-later", categories: ["Network"] } }),
  ],
  plugins: [
    new VitePlugin({
      build: [
        { entry: "src/main/main.ts", config: "vite.main.config.ts", target: "main" },
        { entry: "src/preload/preload.ts", config: "vite.preload.config.ts", target: "preload" },
      ],
      renderer: [{ name: "main_window", config: "vite.renderer.config.ts" }],
    }),
    // Set in the binary at package time: no running it as plain Node, no NODE_OPTIONS or
    // --inspect, and only the app's own ASAR loaded, checked against its hash.
    new FusesPlugin({
      version: FuseVersion.V1,
      [FuseV1Options.RunAsNode]: false,
      [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
      [FuseV1Options.EnableNodeCliInspectArguments]: false,
      [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
      [FuseV1Options.OnlyLoadAppFromAsar]: true,
    }),
  ],
};

export default config;
