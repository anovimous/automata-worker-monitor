import { healthSegments, rangesFor, sourceLabel, toColumns } from "@/charts";
import { matches } from "@/filter";
import { capacityWarning, validate } from "@/newWorker";
import { emptyTemplate, imageProblem, looksSecret, parseSize, sizeText, toInput } from "@/templates";
import type { Template } from "@/types";
import { host, worker } from "./fixtures";

describe("new worker", () => {
  const hosts = [host(), host({ name: "host2", cordoned: true }), host({ name: "host3", reachable: false })];
  it("accepts a good form", () => {
    expect(validate({ host: "host1", templateId: 1, count: 3 }, hosts)).toEqual([]);
  });
  it.each([
    [{ host: "", templateId: 1, count: 1 }, /Choose a host/],
    [{ host: "host2", templateId: 1, count: 1 }, /cordoned/],
    [{ host: "host3", templateId: 1, count: 1 }, /does not answer/],
    [{ host: "host1", templateId: null, count: 1 }, /template/],
    [{ host: "host1", templateId: 1, count: 0 }, /1 to 20/],
    [{ host: "host1", templateId: 1, count: 21 }, /1 to 20/],
    [{ host: "host1", templateId: 1, count: 9 }, /would go over/],
  ])("refuses %o", (form, want) => {
    expect(validate(form, hosts).join(" ")).toMatch(want);
  });
  it("warns before the limits reach the host's capacity", () => {
    const t = { cpus: 2, memoryBytes: 4 * 1024 ** 3 } as Template;
    expect(capacityWarning(host(), t, 1)).toBeNull();
    expect(capacityWarning(host(), t, 2)).toBe("These 2 workers would bring host1 to 8 / 8 CPUs and 16 GiB / 32 GiB.");
  });
});

describe("actions", () => {
  it("keeps the allowed ones in menu order", async () => {
    const { allowedActions, confirmTexts } = await import("@/actions");
    expect(allowedActions(worker({ allowedActions: ["remove", "start"] }))).toEqual(["start", "remove"]);
    expect(allowedActions(worker({ allowedActions: [] }))).toEqual([]);
    expect(confirmTexts.kill).toMatch(/LOSTCONTACT after 30 minutes/);
    expect(confirmTexts.stop).toMatch(/SELFTERMINATED/);
    expect(confirmTexts.undrain).toBeUndefined();
  });
});

describe("filter", () => {
  const w = worker({
    activity: {
      phase: "RUNNING_JOB",
      phaseDetail: "",
      phaseSince: "",
      progressAgeSeconds: 0,
      phaseBudgetSeconds: 0,
      draining: false,
      shuttingDown: false,
      statusAt: "",
      stale: false,
      job: { jobId: 1, routineKey: "fuzz", command: "", state: "RUNNING", takenAt: "", startedAt: null },
    },
  });
  it("matches like the API", () => {
    expect(matches(w, {})).toBe(true);
    expect(matches(w, { routine: "fuzz", phase: "RUNNING_JOB", host: "host1", q: "AAAA" })).toBe(true);
    expect(matches(w, { routine: "masser" })).toBe(false);
    expect(matches(w, { health: "DOWN" })).toBe(false);
  });
});

describe("charts", () => {
  it("aligns series on one time axis with gaps", () => {
    const cols = toColumns([
      { name: "a", unit: "", points: [[60, 1], [120, null]] },
      { name: "b", unit: "", points: [[120, 2], [180, "x"]] },
    ]);
    expect(cols).toEqual([[60, 120, 180], [1, null, null], [null, 2, null]]);
  });
  it("caps the ranges at the retention", () => {
    expect(rangesFor(1).map((r) => r.value)).toEqual(["15m", "1h", "6h", "24h"]);
    expect(rangesFor(14)).toHaveLength(6);
  });
  it("labels the source", () => {
    expect(sourceLabel({ source: "buffer", bucketSeconds: 5, series: [] })).toBe("live, 5 s");
    expect(sourceLabel({ source: "samples", bucketSeconds: 300, series: [] })).toBe("history, 5 min buckets");
    expect(sourceLabel({ source: "samples", bucketSeconds: 3600, series: [] })).toBe("history, 1 h buckets");
  });
  it("merges the health strip into runs", () => {
    const segs = healthSegments({ name: "health", unit: "", points: [[0, "HEALTHY"], [60, "HEALTHY"], [120, null], [180, "DOWN"]] });
    expect(segs).toEqual([
      { from: 0, to: 120, health: "HEALTHY" },
      { from: 120, to: 180, health: null },
      { from: 180, to: 240, health: "DOWN" },
    ]);
  });
});

describe("templates", () => {
  it("parses and writes sizes", () => {
    expect(parseSize("4 GiB")).toBe(4 * 1024 ** 3);
    expect(parseSize("512M")).toBe(512 * 1024 ** 2);
    expect(parseSize("1.5g")).toBe(1.5 * 1024 ** 3);
    expect(parseSize("100")).toBe(100);
    expect(parseSize("4 parsecs")).toBeNull();
    expect(sizeText(1610612736)).toBe("1.5 GiB");
    expect(sizeText(16 * 1024 ** 2)).toBe("16 MiB");
  });
  it("sends only the editable fields of a template", () => {
    const t = { ...emptyTemplate(), id: 5, createdAt: "x", updatedAt: "y", name: "a" } as Template;
    const input = toInput(t);
    expect(Object.keys(input)).not.toContain("id");
    expect(Object.keys(input)).not.toContain("createdAt");
    expect(Object.keys(input).sort()).toEqual(Object.keys(emptyTemplate()).sort());
    expect(input.name).toBe("a");
  });
  it("spots secrets and bad images", () => {
    expect(looksSecret("RABBITMQ_PASSWORD")).toBe(true);
    expect(looksSecret("api_key")).toBe(true);
    expect(looksSecret("LOG_FORMAT")).toBe(false);
    expect(imageProblem("automata-worker:0.4.0")).toBeNull();
    expect(imageProblem("registry:5000/automata-worker:0.4.0")).toBeNull();
    expect(imageProblem("automata-worker@sha256:abc")).toBeNull();
    expect(imageProblem("registry:5000/automata-worker")).toMatch(/tag/);
    expect(imageProblem("automata-worker:latest")).toMatch(/latest/);
  });
});
