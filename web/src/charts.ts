// Chart helpers that need no DOM, so that they can be tested: the ranges, and the timeseries of the API turned into
// the columns uPlot draws.
import type { Range, Series, Timeseries } from "@/types";

export const ranges: { value: Range; label: string; days: number }[] = [
  { value: "15m", label: "15m", days: 0 },
  { value: "1h", label: "1h", days: 0 },
  { value: "6h", label: "6h", days: 1 },
  { value: "24h", label: "24h", days: 1 },
  { value: "7d", label: "7d", days: 7 },
  { value: "14d", label: "14d", days: 14 },
];

// rangesFor leaves out the ranges longer than the retention of the history.
export function rangesFor(retentionDays: number | undefined): typeof ranges {
  return ranges.filter((r) => r.days <= (retentionDays ?? 14));
}

export type Columns = [number[], ...(number | null)[][]];

// toColumns aligns series onto the union of their times; a time a series has no point at is a gap (null).
export function toColumns(series: Series[]): Columns {
  const times = new Set<number>();
  for (const s of series) {
    for (const [t] of s.points) {
      times.add(t);
    }
  }
  const xs = [...times].sort((a, b) => a - b);
  const index = new Map(xs.map((t, i) => [t, i]));
  const cols: (number | null)[][] = series.map((s) => {
    const col: (number | null)[] = new Array(xs.length).fill(null);
    for (const [t, v] of s.points) {
      col[index.get(t)!] = typeof v === "number" ? v : null;
    }
    return col;
  });
  return [xs, ...cols];
}

export function pick(ts: Timeseries | undefined, names: string[]): Series[] {
  if (!ts) {
    return [];
  }
  return names.flatMap((n) => ts.series.filter((s) => s.name === n));
}

export function byPrefix(ts: Timeseries | undefined, prefix: string): Series[] {
  return ts ? ts.series.filter((s) => s.name.startsWith(prefix)) : [];
}

// sourceLabel says where a chart's points come from: "live, 5 s" or "history, 5 min buckets".
export function sourceLabel(ts: Timeseries | undefined): string {
  if (!ts) {
    return "";
  }
  if (ts.source === "buffer") {
    return `live, ${ts.bucketSeconds} s`;
  }
  const b = ts.bucketSeconds;
  return "history, " + (b >= 3600 ? `${b / 3600} h` : `${b / 60} min`) + " buckets";
}

// healthSegments turns the health series into runs of one health, for the strip above the charts.
export function healthSegments(s: Series | undefined): { from: number; to: number; health: string | null }[] {
  if (!s || s.points.length === 0) {
    return [];
  }
  const out: { from: number; to: number; health: string | null }[] = [];
  const pts = s.points;
  const step = pts.length > 1 ? pts[1][0] - pts[0][0] : 60;
  for (let i = 0; i < pts.length; i++) {
    const [t, v] = pts[i];
    const end = i + 1 < pts.length ? pts[i + 1][0] : t + step;
    const h = typeof v === "string" ? v : null;
    const last = out[out.length - 1];
    if (last && last.health === h && last.to === t) {
      last.to = end;
    } else {
      out.push({ from: t, to: end, health: h });
    }
  }
  return out;
}
