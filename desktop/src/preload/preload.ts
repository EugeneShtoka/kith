// The window's whole reach into main: a call and a stream, both by name, both in
// protobuf binary. Main decides which names the window may use; nothing here widens it.
import { contextBridge, ipcRenderer, webUtils } from "electron";

import { channels, type KithBridge, type Offered, type Sent, type StreamEvent, type Subscribe } from "../shared/ipc";

const kith: KithBridge = {
  call: (method, request) => ipcRenderer.invoke(channels.call, method, request) as Promise<Uint8Array>,
  subscribe(method, request, listener) {
    // The port stays here: contextBridge copies data but cannot hand the page a port,
    // so each event is passed on as it comes.
    const { port1, port2 } = new MessageChannel();
    port1.onmessage = (event: MessageEvent<StreamEvent>) => {
      listener(event.data);
    };
    const subscribe: Subscribe = { method, request };
    ipcRenderer.postMessage(channels.subscribe, subscribe, [port2]);
    return () => {
      port1.close();
    };
  },
  unread(count) {
    ipcRenderer.send(channels.unread, count);
  },
  save: (room, event, name) => ipcRenderer.invoke(channels.save, room, event, name) as Promise<boolean>,
  pick: () => ipcRenderer.invoke(channels.pick) as Promise<Offered[]>,
  async offer(file) {
    // The path is the system's, for a file it dropped or pasted: a File the page made
    // itself has none, so the page cannot name one. Without one, the bytes are the file.
    const path = webUtils.getPathForFile(file);
    if (path !== "") {
      return (await ipcRenderer.invoke(channels.offerPath, path)) as Offered;
    }
    return (await ipcRenderer.invoke(
      channels.offerBytes,
      file.name,
      new Uint8Array(await file.arrayBuffer()),
    )) as Offered;
  },
  closing(write) {
    ipcRenderer.on(channels.closing, () => {
      void write().finally(() => {
        ipcRenderer.send(channels.closed);
      });
    });
  },
  discard: (token) => ipcRenderer.invoke(channels.discard, token) as Promise<void>,
  attach: (token, room, caption) => ipcRenderer.invoke(channels.attach, token, room, caption) as Promise<Sent>,
  fontSize: () => ipcRenderer.invoke(channels.fontSize) as Promise<number>,
  setFontSize: (px) => ipcRenderer.invoke(channels.setFontSize, px) as Promise<number>,
};

contextBridge.exposeInMainWorld("kith", kith);
