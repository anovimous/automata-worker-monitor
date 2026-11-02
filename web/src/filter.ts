// The filters of the workers table, the same as the API's ?host=&health=&lifecycle=&phase=&routine=&q= (plan/07 §7),
// kept in the URL.
import type { WorkerView } from "@/types";

export interface WorkerFilter {
  host?: string;
  health?: string;
  lifecycle?: string;
  phase?: string;
  routine?: string;
  q?: string;
}

export function matches(w: WorkerView, f: WorkerFilter): boolean {
  if (f.host && w.host !== f.host) return false;
  if (f.health && w.health !== f.health) return false;
  if (f.lifecycle && w.lifecycle !== f.lifecycle) return false;
  if (f.phase && w.activity?.phase !== f.phase) return false;
  if (f.routine && w.activity?.job?.routineKey !== f.routine) return false;
  if (f.q && !(w.id + " " + w.containerId).toLowerCase().includes(f.q.toLowerCase())) return false;
  return true;
}

export function strings(search: Record<string, unknown>, names: string[]): Record<string, string | undefined> {
  const out: Record<string, string | undefined> = {};
  for (const n of names) {
    const v = search[n];
    out[n] = typeof v === "string" && v !== "" ? v : undefined;
  }
  return out;
}
