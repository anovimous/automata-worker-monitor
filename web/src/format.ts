// Formatters of numbers, sizes and times for the tables and cards.

const units = ["B", "KiB", "MiB", "GiB", "TiB"];

export function bytes(n: number | null | undefined, digits = 1): string {
  if (n === null || n === undefined || !Number.isFinite(n)) {
    return "—";
  }
  let v = Math.abs(n);
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (n < 0 ? "-" : "") + (i === 0 ? String(Math.round(v)) : v.toFixed(digits)) + " " + units[i];
}

export function rate(n: number | null | undefined): string {
  return n === null || n === undefined ? "—" : bytes(n) + "/s";
}

export function cores(n: number | null | undefined): string {
  if (n === null || n === undefined) {
    return "—";
  }
  return n < 0.1 ? n.toFixed(3) : n.toFixed(2);
}

export function percent(ratio: number | null | undefined, digits = 0): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) {
    return "—";
  }
  return (ratio * 100).toFixed(digits) + " %";
}

// duration writes seconds the short way: 45s, 12m, 3h 5m, 2d 4h.
export function duration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) {
    return "—";
  }
  const s = Math.max(0, Math.floor(seconds));
  if (s < 60) {
    return s + "s";
  }
  if (s < 3600) {
    return Math.floor(s / 60) + "m";
  }
  if (s < 86400) {
    const m = Math.floor((s % 3600) / 60);
    return Math.floor(s / 3600) + "h" + (m ? " " + m + "m" : "");
  }
  const h = Math.floor((s % 86400) / 3600);
  return Math.floor(s / 86400) + "d" + (h ? " " + h + "h" : "");
}

export function secondsSince(iso: string | null | undefined, now = Date.now()): number | null {
  if (!iso) {
    return null;
  }
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : (now - t) / 1000;
}

// ago writes how long ago a time was: "12s ago", "never" without one.
export function ago(iso: string | null | undefined, now = Date.now()): string {
  const s = secondsSince(iso, now);
  return s === null ? "never" : duration(s) + " ago";
}

export function dateTime(iso: string | null | undefined): string {
  if (!iso) {
    return "—";
  }
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export function shortId(id: string): string {
  return id.length > 12 ? id.slice(0, 12) : id;
}
