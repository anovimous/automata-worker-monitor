import type { ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Meter } from "@/components/badges";
import { activityText } from "@/activity";
import { ago, bytes, cores, dateTime, duration, rate } from "@/format";
import type { WorkerView } from "@/types";

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 py-0.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right tabular-nums">{children}</span>
    </div>
  );
}

function ofLimit(used: string, limit: string | null): string {
  return limit ? `${used} of ${limit}` : used;
}

// WorkerOverview is the Overview tab. The dependencies card and the connector heartbeat (H4, H5) and the job's own
// resources (R5) are delayed; their reasons still show in the health badge.
export function WorkerOverview({ w }: { w: WorkerView }) {
  const a = w.activity;
  const r = w.resources;
  const budgetRatio = a && a.phaseBudgetSeconds > 0 ? a.progressAgeSeconds / a.phaseBudgetSeconds : null;
  const workDir = w.volumes?.items.find((v) => v.mountPath === "/var/lib/automata/tmp");
  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Activity</CardTitle>
        </CardHeader>
        <CardContent className="space-y-1">
          {!a ? (
            <p className="text-sm text-muted-foreground">No status read yet.</p>
          ) : (
            <>
              <p className="pb-1 text-sm font-medium">
                {activityText(a)}
                {a.stale && <span className="ml-2 text-xs font-normal text-muted-foreground">last known, read {ago(a.statusAt)}</span>}
              </p>
              <Row label="Phase">
                {a.phase}
                {a.phaseDetail && ` (${a.phaseDetail})`}
              </Row>
              <Row label="Since">{ago(a.phaseSince)}</Row>
              <Row label="Progress age">
                {a.progressAgeSeconds} s of a {a.phaseBudgetSeconds} s budget
              </Row>
              <Meter ratio={budgetRatio} />
              <Row label="Draining">{a.draining ? "yes" : "no"}</Row>
              {a.job && (
                <>
                  <div className="pt-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">Current job</div>
                  <Row label="Job">#{a.job.jobId}</Row>
                  <Row label="Routine">{a.job.routineKey || "—"}</Row>
                  <Row label="State">{a.job.state || "—"}</Row>
                  <Row label="Running for">{a.job.startedAt ? duration((Date.now() - Date.parse(a.job.startedAt)) / 1000) : "not started"}</Row>
                  {a.job.command && <Row label="Command">{a.job.command}</Row>}
                </>
              )}
            </>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Resources</CardTitle>
        </CardHeader>
        <CardContent className="space-y-1">
          {!r ? (
            <p className="text-sm text-muted-foreground">The container is not running.</p>
          ) : (
            <>
              <Row label="CPU">{ofLimit(cores(r.cpuCores) + " cores", r.cpuLimit !== null ? cores(r.cpuLimit) : null)}</Row>
              <Meter ratio={r.cpuLimit ? r.cpuCores / r.cpuLimit : null} />
              <Row label="Memory">{ofLimit(bytes(r.memoryWorkingSetBytes), r.memoryLimitBytes !== null ? bytes(r.memoryLimitBytes) : null)}</Row>
              <Meter ratio={r.memoryLimitBytes ? r.memoryWorkingSetBytes / r.memoryLimitBytes : null} />
              <Row label="Pids">{ofLimit(String(r.pids), r.pidsLimit !== null ? String(r.pidsLimit) : null)}</Row>
              <Row label="Block I/O">
                read {rate(r.blockReadBytesPerSec)} · write {rate(r.blockWriteBytesPerSec)}
              </Row>
            </>
          )}
          <Row label="OOM kills">{w.oomKills}</Row>
          <Row label="Restarts">{w.restartCount}</Row>
          <Row label="Last exit code">{w.lastExitCode ?? "—"}</Row>
          <Row label="Uptime">{duration(w.uptimeSeconds)}</Row>
          <Row label="Last seen">{ago(w.lastSeenAt)}</Row>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Disk</CardTitle>
        </CardHeader>
        <CardContent className="space-y-1">
          <Row label="Wordlist cache">
            {w.wordlistCache ? `${bytes(w.wordlistCache.bytes)} of ${bytes(w.wordlistCache.maxBytes)}, ${w.wordlistCache.files} files` : "—"}
          </Row>
          <Meter ratio={w.wordlistCache && w.wordlistCache.maxBytes ? w.wordlistCache.bytes / w.wordlistCache.maxBytes : null} />
          <Row label="Work dir">{bytes(w.workDirBytes ?? workDir?.bytes ?? null)}</Row>
          {w.volumes?.items.map((v) => (
            <Row key={v.name} label={v.name}>
              {bytes(v.bytes)} <span className="text-muted-foreground">at {v.mountPath}</span>
            </Row>
          ))}
          {w.volumes && <p className="pt-1 text-xs text-muted-foreground">Volume sizes from Docker, {ago(w.volumes.sampledAt)}.</p>}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Counters</CardTitle>
        </CardHeader>
        <CardContent className="space-y-1">
          {!w.counters ? (
            <p className="text-sm text-muted-foreground">—</p>
          ) : (
            <>
              <Row label="Messages taken">{w.counters.messagesTaken}</Row>
              <Row label="Messages skipped">{w.counters.messagesSkipped}</Row>
              <Row label="Jobs started">{w.counters.jobsStarted}</Row>
              {Object.entries(w.counters.jobsByOutcome)
                .sort()
                .map(([k, v]) => (
                  <Row key={k} label={`Jobs ${k.toLowerCase()}`}>
                    {v}
                  </Row>
                ))}
              <p className="pt-1 text-xs text-muted-foreground">Since the worker started; created {dateTime(w.createdAt)}.</p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
