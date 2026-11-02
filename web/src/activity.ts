// The activity column (plan/08 §2): what a worker is busy with, in a few words.
import type { Activity } from "@/types";
import { duration, secondsSince } from "@/format";

export function activityText(a: Activity | null, now = Date.now()): string {
  if (!a) {
    return "—";
  }
  const job = a.job;
  const jobName = job ? `job #${job.jobId}${job.routineKey ? ` (${job.routineKey})` : ""}` : "";
  let text: string;
  switch (a.phase) {
    case "STARTING":
      text = "Starting";
      break;
    case "IDLE":
      text = "Idle";
      break;
    case "PREPARING":
      text = job ? `Preparing ${jobName}` : "Preparing";
      if (a.phaseDetail) {
        text += " — " + a.phaseDetail;
      }
      break;
    case "STARTING_JOB":
      text = job ? `Starting ${jobName}` : "Starting a job";
      break;
    case "RUNNING_JOB": {
      const since = secondsSince(job?.startedAt ?? a.phaseSince, now);
      if (job?.state === "PAUSED") {
        text = `Paused ${jobName}`;
      } else {
        text = job ? `Running ${jobName}` : "Running a job";
        if (since !== null) {
          text += " · " + duration(since);
        }
      }
      break;
    }
    case "FINISHING":
      text = job ? `Finishing job #${job.jobId}` : "Finishing";
      break;
    case "DRAINED":
      return "Drained";
    case "SHUTTING_DOWN":
      return "Shutting down";
    default:
      text = a.phase ? a.phase.toLowerCase() : "—";
  }
  if (a.draining && a.phase !== "IDLE") {
    return "Draining — " + text.charAt(0).toLowerCase() + text.slice(1);
  }
  if (a.draining) {
    return "Draining";
  }
  return text;
}
