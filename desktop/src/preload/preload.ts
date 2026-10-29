// The window's whole reach into main: a call and a stream, both by name, both in
// protobuf binary. Main decides which names the window may use; nothing here widens it.
import { contextBridge, ipcRenderer } from "electron";

import { channels, type KithBridge, type StreamEvent, type Subscribe } from "../shared/ipc";

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
};

contextBridge.exposeInMainWorld("kith", kith);
