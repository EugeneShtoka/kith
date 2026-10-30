// The desktop's main process: it starts kith-relay, holds the Connect client and serves
// the window, which reaches kithd only through the gate. See planning/design-electron.md.
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { createConnectTransport } from "@connectrpc/connect-node";
import {
  app,
  BrowserWindow,
  dialog,
  ipcMain,
  type NativeImage,
  nativeImage,
  net,
  protocol,
  session,
  Tray,
} from "electron";

import { channels, type Subscribe } from "../shared/ipc";
import { mediaScheme } from "../shared/media";
import { appURL, fromTheWindow, Gate } from "./gate";
import { Media } from "./media";
import { startRelay } from "./relay";
import { appFile, csp, devCSP } from "./serve";

// Standard and secure, so the window is an origin of its own and loads modules; and the
// window's pictures, which main answers from kithd (kith-media:).
protocol.registerSchemesAsPrivileged([
  { scheme: "app", privileges: { standard: true, secure: true } },
  { scheme: mediaScheme, privileges: { standard: true, secure: true } },
]);

// Electron's own store (caches, storage, the instance lock) is named for the app, which
// would make it the config's directory, ~/.config/kith: it gets one of its own.
app.setPath("userData", path.join(app.getPath("appData"), "kith-desktop"));

let window: BrowserWindow | undefined;

// One window: a second start shows the first.
if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on("second-instance", () => {
    if (window?.isMinimized()) {
      window.restore();
    }
    window?.focus();
  });
  app.on("window-all-closed", () => {
    app.quit();
  });
  app.on("web-contents-created", (_event, contents) => {
    // The window never leaves its page, and opens no other.
    contents.on("will-navigate", (event) => {
      event.preventDefault();
    });
    contents.setWindowOpenHandler(() => ({ action: "deny" }));
  });
  // Nothing the window does needs a browser permission (camera, notifications,
  // clipboard reads): every request and check is refused.
  void app.whenReady().then(() => {
    session.defaultSession.setPermissionRequestHandler((_contents, _permission, answer) => {
      answer(false);
    });
    session.defaultSession.setPermissionCheckHandler(() => false);
  });
  void app.whenReady().then(start).catch(fail);
}

async function start(): Promise<void> {
  const relay = await startRelay(relayFile());
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: "http://kithd",
    nodeOptions: { socketPath: relay.socket },
  });
  const gate = new Gate(transport);

  serveApp();
  const media = new Media((room, event) => gate.media(room, event));
  protocol.handle(mediaScheme, (request) => media.answer(request.url));
  ipcMain.handle(channels.save, (event, room: unknown, id: unknown, name: unknown) =>
    fromTheWindow(event.senderFrame) && typeof room === "string" && typeof id === "string" && typeof name === "string"
      ? save(gate, room, id, name)
      : false,
  );
  ipcMain.handle(channels.call, (event, name: unknown, request: unknown) =>
    gate.call(event.senderFrame, name, request),
  );
  ipcMain.on(channels.subscribe, (event, message: Partial<Subscribe> | undefined) => {
    const [port] = event.ports;
    if (port === undefined) {
      return;
    }
    try {
      const stop = gate.subscribe(event.senderFrame, message?.method, message?.request, (e) => {
        port.postMessage(e);
        if (e.kind === "refused") {
          port.close(); // the gate ended it: the window left its profile
        }
      });
      port.on("close", stop);
      port.start();
    } catch (err) {
      port.postMessage({ kind: "refused", error: err instanceof Error ? err.message : String(err) });
      port.close();
    }
  });
  const tray = new Tray(trayIcon(false));
  tray.setToolTip("kith");
  tray.on("click", () => {
    window?.show();
    window?.focus();
  });
  ipcMain.on(channels.unread, (event, count: unknown) => {
    if (!fromTheWindow(event.senderFrame) || !Number.isSafeInteger(count) || (count as number) < 0) {
      return;
    }
    const n = count as number;
    app.setBadgeCount(n);
    tray.setImage(trayIcon(n > 0));
    tray.setToolTip(n > 0 ? `kith: ${String(n)} unread` : "kith");
  });

  window = new BrowserWindow({
    width: 1000,
    height: 700,
    title: "kith",
    webPreferences: {
      preload: path.join(__dirname, "preload.cjs"),
      // Electron's defaults, said anyway: the window holds what other people wrote.
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
    },
  });
  window.webContents.on("did-start-loading", () => {
    gate.restart();
  });
  await window.loadURL(appURL + "index.html");
}

/** serveApp answers app://kith from the renderer's build, or Vite's server under npm start. */
function serveApp(): void {
  const root = path.join(__dirname, `../renderer/${MAIN_WINDOW_VITE_NAME}`);
  const dev = MAIN_WINDOW_VITE_DEV_SERVER_URL;
  protocol.handle("app", async (request) => {
    const file = appFile(root, request.url);
    if (file === undefined) {
      return new Response(null, { status: 404 });
    }
    // Vite answers by the query and by Accept too (a stylesheet a module imports is served
    // as a module), so under npm start both pass through.
    const response = dev
      ? await net.fetch(new URL(path.relative(root, file) + new URL(request.url).search, dev).toString(), {
          headers: { Accept: request.headers.get("Accept") ?? "*/*" },
        })
      : await net.fetch(pathToFileURL(file).toString());
    const headers = new Headers(response.headers);
    headers.set("Content-Security-Policy", dev ? devCSP : csp);
    return new Response(response.body, { status: response.status, headers });
  });
}

/**
 * trayIcon is the tray's mark, drawn here (the app ships no image yet): a disc, in the
 * accent when the profile has something unread.
 */
function trayIcon(unread: boolean): NativeImage {
  const size = 16;
  const [b, g, r] = unread ? [0x18, 0x23, 0xb4] : [0x80, 0x80, 0x80];
  const pixels = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const inside = (x - 7.5) ** 2 + (y - 7.5) ** 2 <= 6.5 ** 2;
      pixels.set(inside ? [b, g, r, 0xff] : [0, 0, 0, 0], (y * size + x) * 4);
    }
  }
  return nativeImage.createFromBitmap(pixels, { width: size, height: size });
}

/**
 * save writes a message's attachment where the person picks in a save dialog, offered
 * under its own name (its last element only: the name is the sender's). The dialog is
 * the consent: the window names no path.
 */
async function save(gate: Gate, room: string, event: string, name: string): Promise<boolean> {
  const last = path.basename(name.replaceAll("\\", "/"));
  const offered = last === "" || last === "." || last === ".." ? "attachment" : last;
  const options = { defaultPath: path.join(app.getPath("downloads"), offered) };
  const chosen = window ? await dialog.showSaveDialog(window, options) : await dialog.showSaveDialog(options);
  if (chosen.canceled || chosen.filePath === "") {
    return false;
  }
  await writeFile(chosen.filePath, await gate.media(room, event));
  return true;
}

/** relayFile is the kith-relay beside the packaged app, or the repo's (make build-relay). */
function relayFile(): string {
  return app.isPackaged
    ? path.join(process.resourcesPath, "kith-relay")
    : path.join(app.getAppPath(), "..", "kith-relay");
}

function fail(err: unknown): void {
  dialog.showErrorBox("kith", err instanceof Error ? err.message : String(err));
  app.quit();
}
