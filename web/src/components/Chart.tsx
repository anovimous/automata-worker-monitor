import * as React from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { toColumns } from "@/charts";
import { healthSegments } from "@/charts";
import { healthStyles } from "@/health";
import { duration } from "@/format";
import type { Health, Series } from "@/types";

const palette = ["hsl(217 91% 60%)", "hsl(142 65% 42%)", "hsl(280 65% 60%)", "hsl(25 95% 53%)", "hsl(190 80% 45%)", "hsl(0 72% 55%)"];

export interface Line {
  series: Series;
  label?: string;
  color?: string;
  dash?: boolean; // a limit line
}

function cssVar(name: string): string {
  return `hsl(${getComputedStyle(document.documentElement).getPropertyValue(name).trim()})`;
}

// Chart draws lines with uPlot; null values are gaps. format writes the values of the axis and the legend.
export function Chart({
  title,
  lines,
  format,
  height = 160,
  bars = false,
}: {
  title: string;
  lines: Line[];
  format: (v: number) => string;
  height?: number;
  bars?: boolean;
}) {
  const ref = React.useRef<HTMLDivElement>(null);
  const plot = React.useRef<uPlot | null>(null);
  const data = React.useMemo(() => toColumns(lines.map((l) => l.series)), [lines]);
  const empty = data[0].length === 0 || data.slice(1).every((c) => c.every((v) => v === null));

  React.useEffect(() => {
    const el = ref.current;
    if (!el || empty) {
      return;
    }
    const axis = { stroke: cssVar("--muted-foreground"), grid: { stroke: cssVar("--border"), width: 1 }, ticks: { show: false } };
    const opts: uPlot.Options = {
      width: el.clientWidth || 400,
      height,
      legend: { show: true, live: true },
      cursor: { drag: { x: false, y: false } },
      scales: { x: { time: true }, y: { range: (_u, min, max) => [Math.min(0, min), max > 0 ? max * 1.1 : 1] } },
      axes: [axis, { ...axis, size: 70, values: (_u, vals) => vals.map((v) => (v === null ? "" : format(v))) }],
      series: [
        {},
        ...lines.map((l, i) => ({
          label: l.label ?? l.series.name,
          stroke: l.color ?? palette[i % palette.length],
          width: 1.5,
          dash: l.dash ? [4, 4] : undefined,
          spanGaps: false,
          // few values (a new history, a short stay) are dots, else nothing would show
          points: { show: !bars && data[i + 1].filter((v) => v !== null).length <= 60, size: 4 },
          paths: bars ? uPlot.paths.bars!({ size: [0.8, 20] }) : undefined,
          fill: bars ? (l.color ?? palette[i % palette.length]) : undefined,
          value: (_u: uPlot, v: number | null) => (v === null ? "—" : format(v)),
        })),
      ],
    };
    plot.current = new uPlot(opts, data as uPlot.AlignedData, el);
    const ro = new ResizeObserver(() => plot.current?.setSize({ width: el.clientWidth, height }));
    ro.observe(el);
    return () => {
      ro.disconnect();
      plot.current?.destroy();
      plot.current = null;
    };
  }, [data, lines, format, height, bars, empty]);

  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <div className="mb-1 text-xs font-medium text-muted-foreground">{title}</div>
      {empty ? (
        <div className="flex items-center justify-center text-xs text-muted-foreground" style={{ height }}>
          No data in this range
        </div>
      ) : (
        <div ref={ref} className="w-full" />
      )}
    </div>
  );
}

// HealthStrip shows the worst health of each bucket as a coloured strip; gaps stay empty.
export function HealthStrip({ series }: { series: Series | undefined }) {
  const segs = healthSegments(series);
  if (segs.length === 0) {
    return null;
  }
  const from = segs[0].from;
  const span = segs[segs.length - 1].to - from || 1;
  return (
    <div className="space-y-1">
      <div className="relative h-3 w-full overflow-hidden rounded bg-muted">
        {segs.map((s) =>
          s.health ? (
            <div
              key={s.from}
              title={`${healthStyles[s.health as Health]?.label ?? s.health} for ${duration(s.to - s.from)} from ${new Date(s.from * 1000).toLocaleString()}`}
              className="absolute top-0 h-full"
              style={{
                left: `${((s.from - from) / span) * 100}%`,
                width: `${((s.to - s.from) / span) * 100}%`,
                background: healthStyles[s.health as Health]?.color ?? "gray",
              }}
            />
          ) : null,
        )}
      </div>
    </div>
  );
}

// Sparkline is a tiny line of the last points, no axes.
export function Sparkline({ values, max, className }: { values: (number | null)[]; max?: number; className?: string }) {
  const nums = values.filter((v): v is number => v !== null);
  if (nums.length < 2) {
    return <svg className={className} width={64} height={18} />;
  }
  const top = Math.max(max ?? 0, ...nums) || 1;
  const step = 64 / (values.length - 1);
  let d = "";
  values.forEach((v, i) => {
    if (v === null) {
      return;
    }
    d += `${d === "" || values[i - 1] === null ? "M" : "L"}${(i * step).toFixed(1)},${(17 - (v / top) * 16).toFixed(1)}`;
  });
  return (
    <svg className={className} width={64} height={18} viewBox="0 0 64 18" aria-hidden>
      <path d={d} fill="none" stroke="currentColor" strokeWidth={1.2} />
    </svg>
  );
}
