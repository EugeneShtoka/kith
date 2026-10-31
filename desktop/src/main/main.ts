// The desktop's main process: it starts kith-relay, holds the Connect client and serves
// the window, which reaches kithd only through the gate. See planning/design-electron.md.
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { createConnectTransport } from "@connectrpc/connect-node";
import {
  app,
  BrowserWindow,
  clipboard,
  dialog,
  ipcMain,
  type NativeImage,
  nativeImage,
  nativeTheme,
  net,
  protocol,
  session,
  Tray,
} from "electron";

import { BackendService } from "../gen/backend/v1/services_pb";
import { channels, fontSizes, type ThemeMode, themeModes, type Subscribe } from "../shared/ipc";
import { mediaScheme } from "../shared/media";
import { Attachments } from "./attach";
import { appURL, fromTheWindow, Gate } from "./gate";
import { Media } from "./media";
import type { Asks } from "./pairings";
import { type Relay, startRelay } from "./relay";
import { fontSizeCSS, ScaleStore, zoomed, zoomKey } from "./scale";
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

/** How long a closing window has to write its drafts (they wait two seconds at most). */
const closeWait = 2500;

// One window: a second start shows the first.
if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on("second-instance", raise);
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
  let relay = await startRelay(relayFile());
  const through = (r: Relay) =>
    createConnectTransport({
      httpVersion: "1.1",
      baseUrl: "http://kithd",
      nodeOptions: { socketPath: r.socket },
    });
  // A join may have named a bridge in device.toml: a relay started again dials where it
  // says, and the window loads afresh through it. One at a time.
  let rejoining = Promise.resolve();
  const rejoin = () => {
    rejoining = rejoining
      .then(async () => {
        const next = await startRelay(relayFile());
        const was = relay;
        relay = next;
        gate.retarget(through(next));
        was.process.stdin?.end(); // the old relay leaves with its stdin
        window?.webContents.reload();
      })
      .catch(fail);
  };
  // A command's output for the clipboard is copied here: the window holds no clipboard.
  const gate = new Gate(
    through(relay),
    (text) => {
      clipboard.writeText(text).catch((err: unknown) => {
        console.error("kith: a command's output was not copied:", err);
      });
    },
    asks,
    rejoin,
  );

  serveApp();
  const media = new Media((room, event) => gate.media(room, event));
  protocol.handle(mediaScheme, (request) => media.answer(request.url));
  ipcMain.handle(channels.save, (event, room: unknown, id: unknown, name: unknown) =>
    fromTheWindow(event.senderFrame) && typeof room === "string" && typeof id === "string" && typeof name === "string"
      ? save(gate, room, id, name)
      : false,
  );
  // Files to send: main reads them where the person picked or dropped them, or keeps
  // what was pasted, and the window holds only a token for each (attach.ts).
  const attachments = new Attachments((method, input, signal) => gate.own(method, input, signal));
  const fromWindow = (event: Electron.IpcMainInvokeEvent) => {
    if (!fromTheWindow(event.senderFrame)) {
      throw new Error(`kith: a request from ${event.senderFrame?.url ?? "a closed frame"}, not the window`);
    }
  };
  ipcMain.handle(channels.pick, async (event) => {
    fromWindow(event);
    const options: Electron.OpenDialogOptions = { properties: ["openFile", "multiSelections"] };
    const chosen = window ? await dialog.showOpenDialog(window, options) : await dialog.showOpenDialog(options);
    return chosen.canceled ? [] : Promise.all(chosen.filePaths.map((p) => attachments.fromPath(p)));
  });
  ipcMain.handle(channels.offerPath, (event, file: unknown) => {
    fromWindow(event);
    if (typeof file !== "string" || !path.isAbsolute(file)) {
      throw new Error("kith: a dropped file comes with the path the system gave it");
    }
    return attachments.fromPath(file);
  });
  ipcMain.handle(channels.offerBytes, (event, name: unknown, bytes: unknown) => {
    fromWindow(event);
    if (typeof name !== "string" || !(bytes instanceof Uint8Array)) {
      throw new Error("kith: pasted bytes come with a name");
    }
    return attachments.fromBytes(name, bytes);
  });
  ipcMain.handle(channels.discard, (event, token: unknown) => {
    fromWindow(event);
    return typeof token === "string" ? attachments.discard(token) : undefined;
  });
  ipcMain.handle(channels.attach, (event, token: unknown, room: unknown, caption: unknown) => {
    fromWindow(event);
    if (typeof token !== "string" || typeof room !== "string" || typeof caption !== "string") {
      throw new Error("kith: an attachment is sent by its token, to a room");
    }
    return attachments.send(token, room, caption);
  });
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
        if (e.kind === "frames" && message?.method === BackendService.method.followStream.name) {
          raise(); // a link clicked elsewhere (kith --open): the window comes forward to follow it
        }
        if (e.kind === "refused" || e.kind === "ended") {
          port.close(); // the gate ended it (the window left its profile), or it ended for good
        }
      });
      port.on("close", stop);
      port.start();
    } catch (err) {
      port.postMessage({ kind: "refused", error: err instanceof Error ? err.message : String(err) });
      port.close();
    }
  });
  // The window's zoom and text size: Electron's store, not device.toml (decision 5).
  const scale = new ScaleStore(path.join(app.getPath("userData"), "scale.json"));
  await scale.load();
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
  // The theme's mode: the window's light and dark follow what the system is told here.
  ipcMain.on(channels.themeMode, (event, mode: unknown) => {
    if (fromTheWindow(event.senderFrame) && (themeModes as readonly unknown[]).includes(mode)) {
      nativeTheme.themeSource = mode as ThemeMode;
    }
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
      // kithd checks spelling (the TUI's dictionaries and personal words); Chromium's own
      // checker would mark a word twice, and can send words to a remote service.
      spellcheck: false,
    },
  });
  // The page's text size, put in afresh on each load, the one before taken out; and its zoom.
  let fontSizeKey: string | undefined;
  const applyFontSize = async (px: number) => {
    const contents = window?.webContents;
    if (contents === undefined || contents.isDestroyed()) {
      return;
    }
    const was = fontSizeKey;
    fontSizeKey = await contents.insertCSS(fontSizeCSS(px));
    if (was !== undefined) {
      await contents.removeInsertedCSS(was);
    }
  };
  window.webContents.on("dom-ready", () => {
    fontSizeKey = undefined;
    window?.webContents.setZoomFactor(scale.get().zoom);
    void applyFontSize(scale.get().fontSize);
  });
  ipcMain.handle(channels.fontSize, (event) => {
    fromWindow(event);
    return scale.get().fontSize;
  });
  ipcMain.handle(channels.setFontSize, async (event, px: unknown) => {
    fromWindow(event);
    if (typeof px !== "number" || !(fontSizes as readonly number[]).includes(px)) {
      throw new Error("kith: a text size is one the window offers");
    }
    await Promise.all([scale.set({ fontSize: px }), applyFontSize(px)]);
    return px;
  });
  // ctrl+= / ctrl+- / ctrl+0 zoom the window, kept for the next start.
  window.webContents.on("before-input-event", (event, input) => {
    const key = zoomKey(input);
    if (key === undefined) {
      return;
    }
    event.preventDefault();
    const zoom = zoomed(scale.get().zoom, key);
    window?.webContents.setZoomFactor(zoom);
    scale.set({ zoom }).catch(keepFailed);
  });
  window.webContents.on("did-start-loading", () => {
    gate.restart();
    void attachments.abandon(); // nobody waits for those sends any more
  });
  window.on("closed", () => {
    void attachments.abandon();
    gate.endCommands(); // the relay kills a command still running
  });
  // Closing, the window first writes what it holds unsaved (its drafts), as the TUI does
  // at quit; one that does not answer is closed anyway.
  let closing = false;
  // A login the window leads is ended in kithd first, not left waiting on a phone until
  // its deadline (each cancel waits a few seconds at most).
  let ending: Promise<void> = Promise.resolve();
  const close = () => {
    void ending.finally(() => {
      if (window !== undefined && !window.isDestroyed()) {
        window.destroy();
      }
    });
  };
  window.on("close", (event) => {
    if (closing) {
      return;
    }
    closing = true;
    event.preventDefault();
    ending = gate.endLogins();
    window?.webContents.send(channels.closing);
    setTimeout(close, closeWait);
  });
  ipcMain.on(channels.closed, (event) => {
    if (fromTheWindow(event.senderFrame)) {
      close();
    }
  });
  await window.loadURL(appURL + "index.html");
}

/** keepFailed says a scale change was not saved; the window keeps it until it closes. */
function keepFailed(err: unknown): void {
  console.error("kith: the window's zoom or text size was not saved:", err);
}

/**
 * ask shows a question in a dialog of main's own over the window, the safe answer first
 * and taken on Escape; it resolves to whether the person picked the other. With no
 * window, nobody is asked, and the answer is no.
 */
async function ask(options: { message: string; detail: string; no: string; yes: string }): Promise<boolean> {
  if (window === undefined || window.isDestroyed()) {
    return false;
  }
  const { response } = await dialog.showMessageBox(window, {
    type: "warning",
    title: "kith",
    message: options.message,
    detail: options.detail,
    buttons: [options.no, options.yes],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
  });
  return response === 1;
}

/**
 * asks are the questions the gate has main ask before a device is paired, unpaired or a
 * bridge joined (pairings.ts): what is shown is what main read, never the window's words
 * but for names kithd holds.
 */
const asks: Asks = {
  pair: (p) =>
    ask({
      message: `Pair ${p.name === "" ? "this device" : `"${p.name}"`}?`,
      detail:
        `It shows ${p.digits}. Pair it only if the device asking shows the same six digits.\n\n` +
        `Device ID: ${p.id}\n\n` +
        (p.role === "bridge"
          ? "It joins as a bridge: it syncs with this one, and every bridge admits its devices and it theirs. "
          : "") +
        "Once paired, it may do everything this machine may: read and send on every account, " +
        "change the config, pair and unpair devices.",
      no: "Refuse",
      yes: "Pair",
    }),
  unpair: (d, own) =>
    ask({
      message: `Unpair "${d.name === "" ? d.id : d.name}"?`,
      detail:
        `Device ID: ${d.id}\n\n` +
        "Its connections are cut at once, and it cannot connect again until it is paired anew." +
        (own === true
          ? "\n\nThis is the device this window runs on: unpairing it cuts this window off from the bridge."
          : own === undefined
            ? "\n\nkith could not tell whether this is the device this window runs on; if it is, this window is cut off too."
            : ""),
      no: "Keep it",
      yes: "Unpair",
    }),
  join: (i) =>
    ask({
      message: `Join the bridge at ${i.addresses[0] ?? "no address"}?`,
      detail:
        `Bridge ID: ${i.id}\nAddresses: ${i.addresses.join(", ")}\n\n` +
        "Compare the six digits this window then shows with the other device's, and confirm there. " +
        "Once confirmed, this machine's device.toml names the bridge, and this window, " +
        "the terminal client and kith-notify here reach kithd there.",
      no: "Cancel",
      yes: "Join",
    }),
};

/** raise brings the window forward: a second start, or a link to follow. */
function raise(): void {
  if (window?.isMinimized()) {
    window.restore();
  }
  window?.show();
  window?.focus();
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
