// The actions of a worker and their confirmations (the exact meaning matters). Which actions a worker
// allows comes from the control plane (WorkerView.allowedActions); the menu only orders and labels them.
import type { Action, WorkerView } from "@/types";

export const actionOrder: Action[] = ["drain", "undrain", "stop", "restart", "start", "kill", "remove"];

export const actionLabels: Record<Action, string> = {
  drain: "Drain",
  undrain: "Undrain",
  stop: "Stop",
  restart: "Restart",
  start: "Start",
  kill: "Kill",
  remove: "Remove",
};

// the actions that end or interrupt work are confirmed first; drain shows its timeout
export const confirmTexts: Partial<Record<Action, string>> = {
  drain:
    "The worker finishes its current job, takes no new one, then exits. A paused job keeps the worker until it is resumed or canceled.",
  stop: "A running job is ended now and reported to Automata as SELFTERMINATED. A job still being prepared goes back to the queue.",
  kill: "The container is killed at once. A running job is not reported; Automata marks it LOSTCONTACT after 30 minutes. Use Stop unless the worker does not react.",
  remove: "Deletes the container.",
  restart: "Same as Stop, then the container starts again with the same configuration.",
};

export const destructive: Action[] = ["stop", "kill", "remove", "restart"];

export function allowedActions(w: Pick<WorkerView, "allowedActions">): Action[] {
  return actionOrder.filter((a) => w.allowedActions.includes(a));
}

export function drainNote(drainTimeoutSeconds: number | undefined): string {
  if (!drainTimeoutSeconds) {
    return "The drain has no timeout: it waits for the job however long it takes.";
  }
  return `After ${drainTimeoutSeconds} s the drain times out (see the template for what follows).`;
}
