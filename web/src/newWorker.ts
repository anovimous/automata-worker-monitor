// The checks of the new-worker dialog, without the DOM so that they can be tested.
import { bytes } from "@/format";
import type { HostView, Template } from "@/types";

export const maxCount = 20;

export interface NewWorker {
  host: string;
  templateId: number | null;
  count: number;
}

// validate returns what is wrong with the form, nothing when it can be sent.
export function validate(f: NewWorker, hosts: HostView[]): string[] {
  const errors: string[] = [];
  const h = hosts.find((x) => x.name === f.host);
  if (!h) {
    errors.push("Choose a host.");
  } else if (!h.reachable) {
    errors.push(`${h.name} does not answer.`);
  } else if (h.cordoned) {
    errors.push(`${h.name} is cordoned.`);
  }
  if (f.templateId === null) {
    errors.push("Choose a template.");
  }
  if (!Number.isInteger(f.count) || f.count < 1 || f.count > maxCount) {
    errors.push(`The count must be from 1 to ${maxCount}.`);
  } else if (h && h.workers.total + f.count > h.maxWorkers) {
    errors.push(`${h.name} has ${h.workers.total} of its ${h.maxWorkers} workers; ${f.count} more would go over.`);
  }
  return errors;
}

// capacityWarning tells when the new workers' limits would bring the host to or over its CPUs or memory (R7); the
// control plane allows it (the limits are maxima, not reservations), so it is a warning shown before confirming.
export function capacityWarning(h: HostView | undefined, t: Template | undefined, count: number): string | null {
  if (!h || !t || !h.capacity.cpus || count < 1) {
    return null;
  }
  const cpus = h.capacity.cpusReserved + t.cpus * count;
  const mem = h.capacity.memoryBytesReserved + t.memoryBytes * count;
  if (cpus < h.capacity.cpus * 0.9 && mem < h.capacity.memoryBytes * 0.9) {
    return null;
  }
  const n = count === 1 ? "This worker" : `These ${count} workers`;
  return `${n} would bring ${h.name} to ${round(cpus)} / ${h.capacity.cpus} CPUs and ${bytes(mem, 0)} / ${bytes(h.capacity.memoryBytes, 0)}.`;
}

function round(v: number): string {
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

export function freeCapacity(h: HostView): string {
  const cpus = Math.max(0, h.capacity.cpus - h.capacity.cpusReserved);
  const mem = Math.max(0, h.capacity.memoryBytes - h.capacity.memoryBytesReserved);
  return `${round(cpus)} CPUs, ${bytes(mem, 0)} free · ${h.workers.total}/${h.maxWorkers} workers`;
}
