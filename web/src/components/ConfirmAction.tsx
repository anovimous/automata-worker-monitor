import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal } from "lucide-react";
import { api, errorText } from "@/api";
import { actionLabels, allowedActions, confirmTexts, destructive, drainNote } from "@/actions";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Checkbox, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/primitives";
import { Input, Label } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { keys, upsertWorker } from "@/live";
import type { Action, Template, WorkerDetailResponse, WorkerView } from "@/types";

// useAction runs an action of a worker and puts the answer in the cache (the live stream would bring it too).
export function useAction(w: WorkerView) {
  const qc = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: ({ action, body }: { action: Action; body: Record<string, unknown> }) =>
      api.post<WorkerView>(`/workers/${encodeURIComponent(w.id)}/actions/${action}`, body),
    onSuccess: (v, { action }) => {
      if (v && v.id) {
        qc.setQueryData<WorkerView[]>(keys.workers, (old) => upsertWorker(old, v));
      }
      toast({ title: `${actionLabels[action]}: ${w.id}`, variant: "success" });
    },
    onError: (e, { action }) => toast({ title: `${actionLabels[action]} failed: ${w.id}`, description: errorText(e), variant: "destructive" }),
  });
}

// ConfirmDialog asks before an action, with the text of plan/08 §4 and the options of the action.
export function ConfirmDialog({
  worker,
  action,
  template,
  onClose,
}: {
  worker: WorkerView;
  action: Action | null;
  template?: Partial<Template> | null;
  onClose: () => void;
}) {
  const run = useAction(worker);
  // the row menu does not know the template: its drain and stop timeouts come from the worker's template snapshot
  const detail = useQuery({
    queryKey: ["worker-detail", worker.id],
    queryFn: () => api.get<WorkerDetailResponse>(`/workers/${encodeURIComponent(worker.id)}`),
    enabled: !!action && template === undefined,
    staleTime: 60_000,
  });
  const tmpl = template ?? detail.data?.templateSnapshot;
  const [removeVolumes, setRemoveVolumes] = React.useState(false);
  const [timeout, setTimeoutSeconds] = React.useState("");
  React.useEffect(() => {
    setRemoveVolumes(false);
    setTimeoutSeconds("");
  }, [action]);
  if (!action) {
    return null;
  }
  const submit = () => {
    const body: Record<string, unknown> = {};
    if (action === "remove") {
      body.removeVolumes = removeVolumes;
    }
    if ((action === "stop" || action === "restart") && timeout) {
      body.timeoutSeconds = Number(timeout);
    }
    run.mutate({ action, body }, { onSettled: onClose });
  };
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {actionLabels[action]} {worker.id}?
          </DialogTitle>
          <DialogDescription>on {worker.host}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3 px-5 py-4 text-sm">
          <p>{confirmTexts[action]}</p>
          {action === "drain" && <p className="text-muted-foreground">{drainNote(tmpl?.drainTimeoutSeconds)}</p>}
          {action === "remove" && (
            <label className="flex items-center gap-2">
              <Checkbox checked={removeVolumes} onCheckedChange={(v) => setRemoveVolumes(v === true)} />
              Also delete its wordlist cache and temp volumes.
            </label>
          )}
          {(action === "stop" || action === "restart") && (
            <div className="space-y-1">
              <Label htmlFor="stop-timeout">Stop timeout in seconds (empty: the template's)</Label>
              <Input
                id="stop-timeout"
                inputMode="numeric"
                value={timeout}
                onChange={(e) => setTimeoutSeconds(e.target.value.replace(/\D/g, ""))}
                placeholder={tmpl?.stopTimeoutSeconds ? String(tmpl.stopTimeoutSeconds) : ""}
              />
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button variant={destructive.includes(action) ? "destructive" : "default"} onClick={submit} disabled={run.isPending}>
            {actionLabels[action]}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ActionsMenu is the row menu with the actions the worker allows; undrain runs at once, the others are confirmed.
export function ActionsMenu({ worker }: { worker: WorkerView }) {
  const [action, setAction] = React.useState<Action | null>(null);
  const run = useAction(worker);
  const actions = allowedActions(worker);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={`Actions of ${worker.id}`} disabled={actions.length === 0}>
            <MoreHorizontal className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {actions.map((a) => (
            <DropdownMenuItem
              key={a}
              className={destructive.includes(a) ? "text-destructive" : undefined}
              onSelect={() => (confirmTexts[a] ? setAction(a) : run.mutate({ action: a, body: {} }))}
            >
              {actionLabels[a]}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog worker={worker} action={action} onClose={() => setAction(null)} />
    </>
  );
}

// ActionButtons are the same actions as buttons, for the worker page header.
export function ActionButtons({ worker, template }: { worker: WorkerView; template?: Partial<Template> | null }) {
  const [action, setAction] = React.useState<Action | null>(null);
  const run = useAction(worker);
  return (
    <div className="flex flex-wrap gap-1.5">
      {allowedActions(worker).map((a) => (
        <Button
          key={a}
          size="sm"
          variant={destructive.includes(a) ? "outline" : "secondary"}
          className={destructive.includes(a) ? "text-destructive" : undefined}
          onClick={() => (confirmTexts[a] ? setAction(a) : run.mutate({ action: a, body: {} }))}
          disabled={run.isPending}
        >
          {actionLabels[a]}
        </Button>
      ))}
      <ConfirmDialog worker={worker} action={action} template={template} onClose={() => setAction(null)} />
    </div>
  );
}
