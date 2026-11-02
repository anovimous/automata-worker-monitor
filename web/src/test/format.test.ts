import { activityText } from "@/activity";
import { ago, bytes, cores, duration, percent } from "@/format";
import { healthRank, isProblem, lifecycleText, reasonText } from "@/health";
import type { Activity } from "@/types";

const now = Date.parse("2026-11-02T10:12:00Z");

function activity(over: Partial<Activity>): Activity {
  return {
    phase: "IDLE",
    phaseDetail: "",
    phaseSince: "2026-11-02T10:00:00Z",
    progressAgeSeconds: 1,
    phaseBudgetSeconds: 30,
    draining: false,
    shuttingDown: false,
    job: null,
    statusAt: "2026-11-02T10:12:00Z",
    stale: false,
    ...over,
  };
}

const job = { jobId: 4211, routineKey: "fuzz", command: "", state: "RUNNING", takenAt: "", startedAt: "2026-11-02T10:00:00Z" };

describe("activityText", () => {
  it.each([
    [activity({}), "Idle"],
    [activity({ phase: "PREPARING", phaseDetail: "WORDLISTS", job }), "Preparing job #4211 (fuzz) — WORDLISTS"],
    [activity({ phase: "RUNNING_JOB", job }), "Running job #4211 (fuzz) · 12m"],
    [activity({ phase: "RUNNING_JOB", job: { ...job, state: "PAUSED" } }), "Paused job #4211 (fuzz)"],
    [activity({ phase: "FINISHING", job }), "Finishing job #4211"],
    [activity({ phase: "RUNNING_JOB", job, draining: true }), "Draining — running job #4211 (fuzz) · 12m"],
    [activity({ phase: "DRAINED", draining: true }), "Drained"],
    [activity({ phase: "SHUTTING_DOWN" }), "Shutting down"],
  ])("%#", (a, want) => {
    expect(activityText(a, now)).toBe(want);
  });
  it("is a dash without a status", () => expect(activityText(null)).toBe("—"));
});

describe("format", () => {
  it("writes sizes", () => {
    expect(bytes(0)).toBe("0 B");
    expect(bytes(1536)).toBe("1.5 KiB");
    expect(bytes(4 * 1024 ** 3, 0)).toBe("4 GiB");
    expect(bytes(null)).toBe("—");
  });
  it("writes durations", () => {
    expect(duration(45)).toBe("45s");
    expect(duration(720)).toBe("12m");
    expect(duration(3 * 3600 + 300)).toBe("3h 5m");
    expect(duration(2 * 86400 + 4 * 3600)).toBe("2d 4h");
    expect(ago(null)).toBe("never");
    expect(ago("2026-11-02T10:11:48Z", now)).toBe("12s ago");
  });
  it("writes cores and ratios", () => {
    expect(cores(1.734)).toBe("1.73");
    expect(cores(0.0021)).toBe("0.002");
    expect(percent(0.992, 1)).toBe("99.2 %");
    expect(percent(null)).toBe("—");
  });
});

describe("health", () => {
  it("lists the worst first", () => {
    expect(healthRank("DOWN")).toBeLessThan(healthRank("STUCK"));
    expect(healthRank("STUCK")).toBeLessThan(healthRank("HEALTHY"));
  });
  it("counts problems", () => {
    expect(isProblem("DEGRADED")).toBe(true);
    expect(isProblem("HEALTHY")).toBe(false);
    expect(isProblem("NONE")).toBe(false);
    expect(isProblem("STARTING")).toBe(false);
  });
  it("explains reason codes, also unknown ones", () => {
    expect(reasonText("NO_PROGRESS")).toMatch(/no progress/);
    expect(reasonText("HTTP_500")).toContain("HTTP_500");
  });
  it("writes lifecycles", () => {
    expect(lifecycleText("STOPPED", "EXITED_CLEANLY")).toBe("stopped (exited by itself)");
    expect(lifecycleText("PROVISION_FAILED", "")).toBe("provision failed");
  });
});
