import { type ChildProcess, spawn } from "node:child_process";
import { createInterface } from "node:readline";

/** A running kith-relay: its socket, and the process. */
export interface Relay {
  socket: string;
  process: ChildProcess;
}

/**
 * startRelay runs kith-relay at file and resolves once it listens, with the socket it
 * printed. Its stdin is a pipe main holds and never writes: when main exits, crashed or
 * not, the pipe closes and the relay leaves with it.
 */
export function startRelay(file: string): Promise<Relay> {
  const child = spawn(file, [], { stdio: ["pipe", "pipe", "inherit"] });
  return new Promise((resolve, reject) => {
    const exited = (code: number | null, signal: NodeJS.Signals | null) => {
      reject(new Error(`kith-relay exited (${signal ?? String(code)}) before it listened`));
    };
    child.once("error", reject);
    child.once("exit", exited);
    createInterface({ input: child.stdout }).once("line", (line) => {
      child.off("exit", exited);
      resolve({ socket: line.trim(), process: child });
    });
  });
}
