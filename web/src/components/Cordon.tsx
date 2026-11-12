import * as React from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, errorText } from "@/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { keys, upsertHost } from "@/live";
import type { HostView } from "@/types";

// CordonButton cordons a host (no new workers on it, the running ones go on) with a reason, or uncordons it.
export function CordonButton({ host }: { host: HostView }) {
  const qc = useQueryClient();
  const { toast } = useToast();
  const [open, setOpen] = React.useState(false);
  const [reason, setReason] = React.useState("");
  const m = useMutation({
    mutationFn: (cordon: boolean) =>
      api.post<HostView>(`/hosts/${encodeURIComponent(host.name)}/${cordon ? "cordon" : "uncordon"}`, cordon ? { reason } : {}),
    onSuccess: (h) => {
      qc.setQueryData<HostView[]>(keys.hosts, (old) => upsertHost(old, h));
      setOpen(false);
    },
    onError: (e) => toast({ title: "Failed", description: errorText(e), variant: "destructive" }),
  });
  if (host.cordoned) {
    return (
      <Button size="sm" variant="outline" onClick={() => m.mutate(false)} disabled={m.isPending}>
        Uncordon
      </Button>
    );
  }
  return (
    <>
      <Button size="sm" variant="outline" onClick={() => (setReason(""), setOpen(true))}>
        Cordon
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Cordon {host.name}?</DialogTitle>
            <DialogDescription>No new workers are created on it; the workers on it keep running.</DialogDescription>
          </DialogHeader>
          <div className="space-y-1 px-5 py-4">
            <Label htmlFor="cordon-reason">Reason</Label>
            <Input id="cordon-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. disk replacement" />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => m.mutate(true)} disabled={m.isPending}>
              Cordon
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
