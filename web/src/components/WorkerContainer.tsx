import type { ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { bytes, dateTime } from "@/format";
import type { WorkerDetailResponse } from "@/types";

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(8rem,14rem)_1fr] gap-3 py-0.5 text-sm">
      <span className="break-all text-muted-foreground">{label}</span>
      <span className="break-all font-mono text-xs leading-5">{children}</span>
    </div>
  );
}

// WorkerContainer is the Container tab: what Docker says of the container. The env shows keys only: the values hold
// the worker's credentials and never leave the host.
export function WorkerContainer({ detail }: { detail: WorkerDetailResponse }) {
  const c = detail.container;
  const w = detail.worker;
  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Container</CardTitle>
        </CardHeader>
        <CardContent>
          {!c ? (
            <p className="text-sm text-muted-foreground">{detail.containerError || "The worker machine controller gave no details."}</p>
          ) : (
            <>
              <Row label="Container id">{c.container.id}</Row>
              <Row label="Name">{c.container.name}</Row>
              <Row label="Image">{c.container.image}</Row>
              <Row label="Image id">{c.details.imageId}</Row>
              <Row label="Created">{dateTime(c.details.created)}</Row>
              <Row label="State">
                {c.container.state} — {c.container.status}
              </Row>
              <Row label="Network">
                {c.container.networkMode}
                {c.container.ip && ` · ${c.container.ip}`}
              </Row>
              <Row label="Telemetry">{c.telemetry.configured ? c.telemetry.url : "not configured"}</Row>
              <Row label="Limits">
                {c.container.limits.cpus ?? "—"} CPUs · {bytes(c.container.limits.memoryBytes)} · {c.container.limits.pids ?? "—"} pids
              </Row>
              <Row label="Restart policy">
                {c.details.restartPolicy.name}
                {c.details.restartPolicy.maxRetries ? ` (max ${c.details.restartPolicy.maxRetries})` : ""}
              </Row>
              <Row label="Stop timeout">{c.details.stopTimeoutSeconds ?? "—"} s</Row>
            </>
          )}
        </CardContent>
      </Card>
      {c && (
        <Card>
          <CardHeader>
            <CardTitle>Mounts and env keys</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div>
              {c.details.mounts.map((m) => (
                <Row key={m.destination} label={m.type}>
                  {m.source || "—"} → {m.destination}
                </Row>
              ))}
            </div>
            <div>
              <div className="pb-1 text-xs text-muted-foreground">Env keys (values are not shown)</div>
              <div className="flex flex-wrap gap-1">
                {c.details.envKeys.map((k) => (
                  <span key={k} className="rounded border border-border px-1.5 py-0.5 font-mono text-xs">
                    {k}
                  </span>
                ))}
              </div>
            </div>
          </CardContent>
        </Card>
      )}
      {c && (
        <Card>
          <CardHeader>
            <CardTitle>Labels</CardTitle>
          </CardHeader>
          <CardContent>
            {Object.entries(c.container.labels)
              .sort()
              .map(([k, v]) => (
                <Row key={k} label={k}>
                  {v}
                </Row>
              ))}
          </CardContent>
        </Card>
      )}
      <Card>
        <CardHeader>
          <CardTitle>Template snapshot</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="pb-2 text-xs text-muted-foreground">
            The template {w.template ? `"${w.template}"` : ""} as it was when the worker was created; later edits do not change it.
          </p>
          <pre className="max-h-96 overflow-auto rounded-md bg-muted/40 p-2 text-xs">{JSON.stringify(detail.templateSnapshot, null, 2)}</pre>
        </CardContent>
      </Card>
    </div>
  );
}
