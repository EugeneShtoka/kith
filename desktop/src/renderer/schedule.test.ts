import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import table from "../../../internal/tui/testdata/schedule.json";
import { ScheduledMessageSchema } from "../gen/backend/v1/services_pb";
import { duration, parseSchedule, rows, type When } from "./schedule";

interface Case {
  kind: When;
  arg: string;
  at?: string;
  body?: string;
  error?: string;
}

/** wall is a time as the table writes it, in the window's zone. */
function wall(at: Date): string {
  const two = (n: number) => String(n).padStart(2, "0");
  return (
    `${String(at.getFullYear())}-${two(at.getMonth() + 1)}-${two(at.getDate())}` +
    `T${two(at.getHours())}:${two(at.getMinutes())}:${two(at.getSeconds())}`
  );
}

/** local is a wall time of the table's in the window's zone. */
function local(text: string): Date {
  const [d = "", t = ""] = text.split("T");
  const [y, mo, day] = d.split("-").map(Number);
  const [h, mi, s] = t.split(":").map(Number);
  return new Date(y ?? 0, (mo ?? 1) - 1, day ?? 1, h ?? 0, mi ?? 0, s ?? 0);
}

// The same table internal/tui's conformance test holds to parseScheduleTime, read in
// this zone as Go reads it in its own.
describe("a time to send at, as the TUI reads /at and /in", () => {
  const now = local(table.now);
  for (const c of table.cases as Case[]) {
    it(`/${c.kind} ${JSON.stringify(c.arg)}`, () => {
      const got = parseSchedule(c.kind, c.arg, now);
      if (c.error !== undefined) {
        expect(got).toEqual({ error: c.error });
      } else {
        expect("at" in got ? { at: wall(got.at), body: got.body } : got).toEqual({ at: c.at, body: c.body });
      }
    });
  }
});

describe("a delay", () => {
  it("is Go's: a sign, numbers each with its unit, fractions", () => {
    expect(duration("1h30m")).toBe(90 * 60_000);
    expect(duration("-1.5s")).toBe(-1500);
    expect(duration("0")).toBe(0);
    expect(duration("")).toBeUndefined();
    expect(duration("1")).toBeUndefined();
    expect(duration("1d")).toBeUndefined();
    expect(duration("3000000h")).toBeUndefined(); // past what a Go duration holds
  });
});

describe("the queue as listed", () => {
  const now = new Date(2026, 8, 1, 14, 30);
  const at = (d: Date) => BigInt(d.getTime());
  const queue = [
    create(ScheduledMessageSchema, {
      id: "s1",
      roomId: "!a",
      body: "later",
      atMs: at(new Date(2026, 8, 1, 15, 0)),
      writtenMs: at(now),
    }),
    create(ScheduledMessageSchema, {
      id: "s2",
      roomId: "!b",
      body: "x".repeat(60),
      atMs: at(new Date(2026, 8, 1, 14, 0)),
      writtenMs: at(new Date(2026, 8, 1, 13, 0)),
      failing: "homeserver unreachable",
    }),
    create(ScheduledMessageSchema, {
      id: "s3",
      roomId: "!a",
      body: "went wrong",
      atMs: at(new Date(2026, 8, 1, 13, 0)),
      writtenMs: at(new Date(2026, 8, 1, 13, 0)),
      refused: "M_TOO_LARGE",
    }),
  ];

  it("is one room's, or every room's, each saying when, and why one waits", () => {
    expect(rows(queue, "!a", now).map((r) => r.id)).toEqual(["s1", "s3"]);
    const all = rows(queue, "", now);
    expect(all.map((r) => [r.id, r.problem, r.reason])).toEqual([
      ["s1", "", ""],
      ["s2", "failing", "homeserver unreachable"],
      ["s3", "refused", "M_TOO_LARGE"],
    ]);
    expect(all[0]?.summary).toMatch(/ — later$/);
    expect(all[1]?.summary).toMatch(/\(overdue\) — x{47}…$/);
    expect(all[2]?.summary).toMatch(/^unsent since /);
  });
});
