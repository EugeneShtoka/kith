// The desktop's main process: it starts kith-relay, holds the Connect client and serves
// the window, which reaches kithd only through the gate. See planning/design-electron.md.
import path from "node:path";
import { pathToFileURL } from "node:url";

import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { app, BrowserWindow, dialog, ipcMain, net, protocol, session } from "electron";

import { DeviceService } from "../gen/device/v1/device_pb";
import { channels, type Subscribe } from "../shared/ipc";
import { appURL, Gate } from "./gate";
import { startRelay } from "./relay";
import { appFile, csp, devCSP } from "./serve";

// Standard and secure, so the window is an origin of its own and loads modules.
protocol.registerSchemesAsPrivileged([{ scheme: "app", privileges: { standard: true, secure: true } }]);

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
  const device = await createClient(DeviceService, transport).device({});
  const gate = new Gate(transport, { "Kith-Profile": device.profile });

  serveApp();
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
      });
      port.on("close", stop);
      port.start();
    } catch (err) {
      port.postMessage({ kind: "refused", error: err instanceof Error ? err.message : String(err) });
      port.close();
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
    },
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
