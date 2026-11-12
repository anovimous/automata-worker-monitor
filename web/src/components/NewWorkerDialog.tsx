import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorText } from "@/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { bytes } from "@/format";
import { keys, useHosts } from "@/live";
import { capacityWarning, freeCapacity, maxCount, validate } from "@/newWorker";
import type { CreateResult, Template } from "@/types";

const selectClass = "h-8 w-full rounded-md border border-input bg-transparent px-2 text-sm";

export function NewWorkerDialog({ open, onOpenChange, host: fixedHost }: { open: boolean; onOpenChange: (o: boolean) => void; host?: string }) {
  const { data: hosts = [] } = useHosts();
  const { data: templates = [] } = useQuery({
    queryKey: ["templates"],
    queryFn: () => api.get<Template[]>("/templates"),
    enabled: open,
  });
  const usable = hosts.filter((h) => h.reachable && !h.cordoned);
  const [host, setHost] = React.useState(fixedHost ?? "");
  const [templateId, setTemplateId] = React.useState<number | null>(null);
  const [count, setCount] = React.useState("1");
  React.useEffect(() => {
    if (open) {
      setHost(fixedHost ?? usable[0]?.name ?? "");
      setTemplateId(templates[0]?.id ?? null);
      setCount("1");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  React.useEffect(() => {
    if (templateId === null && templates.length > 0) setTemplateId(templates[0].id);
  }, [templates, templateId]);

  const qc = useQueryClient();
  const { toast } = useToast();
  const create = useMutation({
    mutationFn: () => api.post<CreateResult>("/workers", { host, templateId, count: Number(count) }),
    onSuccess: (res) => {
      for (const w of res.workers) {
        toast(
          w.ok
            ? { title: `Created ${w.id}`, description: "It shows as STARTING until its first good status.", variant: "success" }
            : { title: `Failed to create ${w.id}`, description: w.error, variant: "destructive" },
        );
      }
      for (const warning of res.warnings ?? []) {
        toast({ title: "Capacity", description: warning, variant: "warning" });
      }
      qc.invalidateQueries({ queryKey: keys.workers });
      onOpenChange(false);
    },
    onError: (e) => toast({ title: "The workers were not created", description: errorText(e), variant: "destructive" }),
  });

  const n = Number(count);
  const errors = validate({ host, templateId, count: n }, hosts);
  const h = hosts.find((x) => x.name === host);
  const t = templates.find((x) => x.id === templateId);
  const warning = capacityWarning(h, t, n);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New worker</DialogTitle>
          <DialogDescription>Creates and starts containers from a template on a host.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 px-5 py-4 text-sm">
          <div className="space-y-1">
            <Label htmlFor="nw-host">Host</Label>
            <select id="nw-host" className={selectClass} value={host} onChange={(e) => setHost(e.target.value)} disabled={!!fixedHost}>
              {usable.length === 0 && <option value="">No reachable host that is not cordoned</option>}
              {usable.map((x) => (
                <option key={x.name} value={x.name}>
                  {x.name} — {freeCapacity(x)}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1">
            <Label htmlFor="nw-template">Template</Label>
            <select
              id="nw-template"
              className={selectClass}
              value={templateId ?? ""}
              onChange={(e) => setTemplateId(e.target.value ? Number(e.target.value) : null)}
            >
              {templates.length === 0 && <option value="">No template</option>}
              {templates.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </select>
            {t && (
              <p className="text-xs text-muted-foreground">
                <span className="font-mono">{t.image}</span> · {t.cpus} CPUs · {bytes(t.memoryBytes, 0)} · network {t.network} · env file{" "}
                {t.envFile}
              </p>
            )}
          </div>
          <div className="space-y-1">
            <Label htmlFor="nw-count">Count (1–{maxCount})</Label>
            <Input id="nw-count" inputMode="numeric" value={count} onChange={(e) => setCount(e.target.value.replace(/\D/g, ""))} className="w-24" />
          </div>
          {warning && <p className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">{warning}</p>}
          {errors.length > 0 && (
            <ul className="space-y-0.5 text-xs text-destructive">
              {errors.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={() => create.mutate()} disabled={errors.length > 0 || create.isPending}>
            {create.isPending ? "Creating…" : `Create ${n > 1 ? n + " workers" : "worker"}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
