import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { api, errorText } from "@/api";
import { ErrorBox, PageTitle } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label, Textarea } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/primitives";
import { useToast } from "@/components/ui/toast";
import { bytes, dateTime } from "@/format";
import { useHosts } from "@/live";
import { emptyTemplate, fromPairs, imageProblem, looksSecret, pairs, parseSize, sizeText, toInput } from "@/templates";
import type { Template, TemplateInput } from "@/types";

const th = "px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wide text-muted-foreground whitespace-nowrap";
const td = "px-3 py-2 whitespace-nowrap";
const selectClass = "h-8 w-full rounded-md border border-input bg-transparent px-2 text-sm";

function Field({ label, hint, children, htmlFor }: { label: string; hint?: string; children: React.ReactNode; htmlFor?: string }) {
  return (
    <div className="space-y-1">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

function SizeInput({ id, value, onChange }: { id: string; value: number; onChange: (n: number) => void }) {
  const [text, setText] = React.useState(sizeText(value));
  React.useEffect(() => setText(sizeText(value)), [value]);
  const parsed = parseSize(text);
  return (
    <>
      <Input
        id={id}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={() => (parsed !== null ? onChange(parsed) : setText(sizeText(value)))}
        className={parsed === null ? "border-destructive" : undefined}
      />
    </>
  );
}

function MapRows({ rows, setRows, warn }: { rows: [string, string][]; setRows: (r: [string, string][]) => void; warn?: (k: string) => string | null }) {
  return (
    <div className="space-y-1">
      {rows.map(([k, v], i) => (
        <div key={i} className="space-y-0.5">
          <div className="flex gap-1">
            <Input value={k} placeholder="key" className="font-mono" onChange={(e) => setRows(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))} />
            <Input value={v} placeholder="value" className="font-mono" onChange={(e) => setRows(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
            <Button variant="ghost" size="icon" aria-label="Remove the row" onClick={() => setRows(rows.filter((_, j) => j !== i))}>
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
          {warn?.(k) && <p className="text-xs text-warning">{warn(k)}</p>}
        </div>
      ))}
      <Button variant="outline" size="sm" onClick={() => setRows([...rows, ["", ""]])}>
        <Plus className="h-3 w-3" /> Add
      </Button>
    </div>
  );
}

function TemplateEditor({ editing, onClose }: { editing: Template | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useToast();
  const { data: hosts = [] } = useHosts();
  const reachable = hosts.filter((h) => h.reachable);
  const [host, setHost] = React.useState("");
  const [t, setT] = React.useState<TemplateInput>(emptyTemplate());
  const [env, setEnv] = React.useState<[string, string][]>([]);
  const [labels, setLabels] = React.useState<[string, string][]>([]);
  const [extraHosts, setExtraHosts] = React.useState("");
  React.useEffect(() => {
    if (!editing) return;
    const base: TemplateInput = editing === "new" ? emptyTemplate() : toInput(editing);
    setT(base);
    setEnv(pairs(base.env));
    setLabels(pairs(base.extraLabels));
    setExtraHosts(base.extraHosts.join("\n"));
    setHost(reachable[0]?.name ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing]);
  const envFiles = useQuery({
    queryKey: ["env-files", host],
    queryFn: () => api.get<string[]>(`/hosts/${encodeURIComponent(host)}/env-files`),
    enabled: !!editing && !!host,
  });
  const images = useQuery({
    queryKey: ["images", host],
    queryFn: () => api.get<{ id: string; tags: string[] }[]>(`/hosts/${encodeURIComponent(host)}/images`),
    enabled: !!editing && !!host,
  });
  const save = useMutation({
    mutationFn: (body: TemplateInput) =>
      editing === "new" || !editing ? api.post<Template>("/templates", body) : api.put<Template>(`/templates/${editing.id}`, body),
    onSuccess: (saved) => {
      qc.invalidateQueries({ queryKey: ["templates"] });
      toast({ title: `Template ${saved.name} saved`, variant: "success" });
      onClose();
    },
  });
  const set = <K extends keyof TemplateInput>(k: K, v: TemplateInput[K]) => setT((old) => ({ ...old, [k]: v }));
  const num = (k: keyof TemplateInput) => (e: React.ChangeEvent<HTMLInputElement>) => set(k, Number(e.target.value) as never);
  const problem = imageProblem(t.image);
  const submit = () =>
    save.mutate({
      ...t,
      env: fromPairs(env),
      extraLabels: fromPairs(labels),
      extraHosts: extraHosts.split(/\s+/).filter(Boolean),
    });
  const tags = (images.data ?? []).flatMap((i) => i.tags).sort();

  return (
    <Dialog open={!!editing} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{editing === "new" ? "New template" : `Edit template ${t.name}`}</DialogTitle>
          <DialogDescription>Workers keep the template as it was when they were created; an edit applies to new workers.</DialogDescription>
        </DialogHeader>
        <div className="grid max-h-[70vh] gap-4 overflow-y-auto px-5 py-4 md:grid-cols-2">
          <Field label="Name" htmlFor="t-name" hint="Lowercase letters, digits and -.">
            <Input id="t-name" value={t.name} onChange={(e) => set("name", e.target.value)} />
          </Field>
          <Field label="Lists of images and env files from host" htmlFor="t-host">
            <select id="t-host" className={selectClass} value={host} onChange={(e) => setHost(e.target.value)}>
              {reachable.length === 0 && <option value="">No reachable host</option>}
              {reachable.map((h) => (
                <option key={h.name}>{h.name}</option>
              ))}
            </select>
          </Field>
          <div className="md:col-span-2">
            <Field label="Description" htmlFor="t-desc">
              <Textarea id="t-desc" rows={2} value={t.description} onChange={(e) => set("description", e.target.value)} />
            </Field>
          </div>
          <Field label="Image" htmlFor="t-image" hint={problem ?? undefined}>
            <Input id="t-image" list="t-images" className="font-mono" value={t.image} onChange={(e) => set("image", e.target.value)} />
            <datalist id="t-images">
              {tags.map((tag) => (
                <option key={tag} value={tag} />
              ))}
            </datalist>
          </Field>
          <Field label="Pull policy" htmlFor="t-pull">
            <select id="t-pull" className={selectClass} value={t.pullPolicy} onChange={(e) => set("pullPolicy", e.target.value as TemplateInput["pullPolicy"])}>
              <option value="if-missing">if-missing</option>
              <option value="always">always</option>
              <option value="never">never</option>
            </select>
          </Field>
          <Field label="Env file" htmlFor="t-envfile" hint="Each host needs an env file of this name; it holds the worker's secrets.">
            <Input id="t-envfile" list="t-envfiles" value={t.envFile} onChange={(e) => set("envFile", e.target.value)} />
            <datalist id="t-envfiles">
              {(envFiles.data ?? []).map((f) => (
                <option key={f} value={f} />
              ))}
            </datalist>
          </Field>
          <Field label="Network" htmlFor="t-network" hint="host, or the name of a Docker network.">
            <Input id="t-network" value={t.network} onChange={(e) => set("network", e.target.value)} />
          </Field>
          <Field label="CPUs" htmlFor="t-cpus">
            <Input id="t-cpus" type="number" step="0.1" min="0.1" value={t.cpus} onChange={num("cpus")} />
          </Field>
          <Field label="Memory" htmlFor="t-mem" hint={bytes(t.memoryBytes)}>
            <SizeInput id="t-mem" value={t.memoryBytes} onChange={(n) => set("memoryBytes", n)} />
          </Field>
          <Field label="Pids limit" htmlFor="t-pids">
            <Input id="t-pids" type="number" min="1" value={t.pidsLimit} onChange={num("pidsLimit")} />
          </Field>
          <Field label="Wordlist cache size" htmlFor="t-cache" hint="Each worker has its own cache volume.">
            <SizeInput id="t-cache" value={t.wordlistCacheMaxBytes} onChange={(n) => set("wordlistCacheMaxBytes", n)} />
          </Field>
          <Field label="Sockets tmpfs size" htmlFor="t-tmpfs">
            <SizeInput id="t-tmpfs" value={t.tmpfsSocketsBytes} onChange={(n) => set("tmpfsSocketsBytes", n)} />
          </Field>
          <Field label="Restart retries" htmlFor="t-retries" hint="Docker restarts a crashed worker this many times.">
            <Input id="t-retries" type="number" min="0" value={t.restartMaxRetries} onChange={num("restartMaxRetries")} />
          </Field>
          <Field label="Stop timeout (s)" htmlFor="t-stop">
            <Input id="t-stop" type="number" min="1" value={t.stopTimeoutSeconds} onChange={num("stopTimeoutSeconds")} />
          </Field>
          <Field label="Drain timeout (s)" htmlFor="t-drain" hint="0: no timeout.">
            <Input id="t-drain" type="number" min="0" value={t.drainTimeoutSeconds} onChange={num("drainTimeoutSeconds")} />
          </Field>
          <div className="space-y-2 pt-5">
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={t.escalateAfterDrainTimeout} onCheckedChange={(v) => set("escalateAfterDrainTimeout", v === true)} />
              Stop the worker when the drain times out
            </label>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={t.removeAfterDrain} onCheckedChange={(v) => set("removeAfterDrain", v === true)} />
              Remove the worker after a drain
            </label>
          </div>
          <Field label="Env overrides" hint="Not secret values only.">
            <MapRows rows={env} setRows={setEnv} warn={(k) => (looksSecret(k) ? "This looks like a secret: put secrets in the env file on the host instead." : null)} />
          </Field>
          <Field label="Extra labels">
            <MapRows rows={labels} setRows={setLabels} />
          </Field>
          <div className="md:col-span-2">
            <Field label="Extra hosts" htmlFor="t-hosts" hint="One name:address per line.">
              <Textarea id="t-hosts" rows={2} className="font-mono" value={extraHosts} onChange={(e) => setExtraHosts(e.target.value)} />
            </Field>
          </div>
          {save.error && (
            <p className="text-sm text-destructive md:col-span-2">{errorText(save.error)}</p>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={!!problem || !t.name || save.isPending}>
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function TemplatesPage() {
  const qc = useQueryClient();
  const { toast } = useToast();
  const { data = [], error } = useQuery({ queryKey: ["templates"], queryFn: () => api.get<Template[]>("/templates") });
  const [editing, setEditing] = React.useState<Template | "new" | null>(null);
  const del = useMutation({
    mutationFn: (t: Template) => api.del(`/templates/${t.id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["templates"] }),
    onError: (e) => toast({ title: "Not deleted", description: errorText(e), variant: "destructive" }),
  });
  return (
    <div className="space-y-4">
      <PageTitle
        right={
          <Button onClick={() => setEditing("new")}>
            <Plus className="h-4 w-4" /> New template
          </Button>
        }
      >
        Templates
      </PageTitle>
      <ErrorBox error={error} />
      <div className="overflow-x-auto rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead className="border-b border-border bg-muted/40">
            <tr>
              <th className={th}>Name</th>
              <th className={th}>Image</th>
              <th className={th}>Limits</th>
              <th className={th}>Network</th>
              <th className={th}>Env file</th>
              <th className={th}>Drain</th>
              <th className={th}>Updated</th>
              <th className={th} />
            </tr>
          </thead>
          <tbody>
            {data.map((t) => (
              <tr key={t.id} className="border-b border-border last:border-0">
                <td className={td}>
                  <div className="font-medium">{t.name}</div>
                  {t.description && <div className="max-w-xs truncate text-xs text-muted-foreground">{t.description}</div>}
                </td>
                <td className={`${td} font-mono text-xs`}>{t.image}</td>
                <td className={td}>
                  {t.cpus} CPUs · {bytes(t.memoryBytes, 0)} · {t.pidsLimit} pids
                </td>
                <td className={td}>{t.network}</td>
                <td className={td}>{t.envFile}</td>
                <td className={td}>
                  {t.drainTimeoutSeconds ? `${t.drainTimeoutSeconds} s${t.escalateAfterDrainTimeout ? ", then stop" : ""}` : "no timeout"}
                  {t.removeAfterDrain && ", then remove"}
                </td>
                <td className={td}>{dateTime(t.updatedAt)}</td>
                <td className={`${td} text-right`}>
                  <Button variant="ghost" size="icon-sm" aria-label={`Edit ${t.name}`} onClick={() => setEditing(t)}>
                    <Pencil className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Delete ${t.name}`}
                    onClick={() => confirm(`Delete the template ${t.name}? Workers made from it keep their own copy.`) && del.mutate(t)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <TemplateEditor editing={editing} onClose={() => setEditing(null)} />
    </div>
  );
}
