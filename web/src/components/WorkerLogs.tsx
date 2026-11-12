import * as React from "react";
import { api, errorText } from "@/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/primitives";
import { cn } from "@/lib/cn";
import type { LogLine } from "@/types";

const tail = 500;
const keep = 5000; // lines kept in the page while following

// WorkerLogs is the Logs tab: the last 500 lines, then the stream when following; the filter is on the page only.
export function WorkerLogs({ id }: { id: string }) {
  const [lines, setLines] = React.useState<LogLine[]>([]);
  const [follow, setFollow] = React.useState(true);
  const [paused, setPaused] = React.useState(false);
  const [filter, setFilter] = React.useState("");
  const [error, setError] = React.useState("");
  const box = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    setError("");
    if (!follow) {
      api
        .get<LogLine[]>(`/workers/${encodeURIComponent(id)}/logs?tail=${tail}`)
        .then(setLines)
        .catch((e) => setError(errorText(e)));
      return;
    }
    setLines([]);
    const es = new EventSource(`/api/v1/workers/${encodeURIComponent(id)}/logs?tail=${tail}&follow=true`);
    es.onmessage = (ev) => {
      try {
        const l = JSON.parse(ev.data) as LogLine;
        setLines((old) => (old.length >= keep ? [...old.slice(old.length - keep + 1), l] : [...old, l]));
      } catch {
        // not a log line
      }
    };
    es.onerror = () => setError("The log stream was interrupted; it reconnects by itself.");
    return () => es.close();
  }, [id, follow]);

  const shown = React.useMemo(() => {
    const f = filter.toLowerCase();
    return f ? lines.filter((l) => l.line.toLowerCase().includes(f)) : lines;
  }, [lines, filter]);

  React.useEffect(() => {
    if (!paused && box.current) {
      box.current.scrollTop = box.current.scrollHeight;
    }
  }, [shown, paused]);

  const download = () => {
    const blob = new Blob([shown.map((l) => `${l.ts} ${l.stream} ${l.line}`).join("\n") + "\n"], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `${id}-logs.txt`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="space-y-2">
      <p className="rounded-md border border-warning/30 bg-warning/10 px-3 py-1.5 text-xs text-warning">
        Lines are redacted for credentials; treat them as sensitive anyway.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={follow} onCheckedChange={setFollow} /> Follow
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={paused} onCheckedChange={setPaused} /> Pause scrolling
        </label>
        <Input className="h-8 w-64" placeholder="Filter lines" value={filter} onChange={(e) => setFilter(e.target.value)} />
        <span className="text-xs text-muted-foreground">
          {shown.length} of {lines.length} lines
        </span>
        <Button variant="outline" size="sm" className="ml-auto" onClick={download} disabled={shown.length === 0}>
          Download .txt
        </Button>
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div ref={box} className="h-[60vh] overflow-auto rounded-lg border border-border bg-muted/30 p-2 font-mono text-xs leading-5">
        {shown.map((l, i) => (
          <div key={i} className="whitespace-pre-wrap break-all">
            <span className="select-none text-muted-foreground">{new Date(l.ts).toLocaleTimeString()} </span>
            {/* the worker logs to stderr; the tag tells the streams apart without painting every line red */}
            <span className={cn("select-none", l.stream === "stderr" ? "text-warning" : "text-muted-foreground")}>
              {l.stream === "stderr" ? "err " : "out "}
            </span>
            {l.line}
          </div>
        ))}
      </div>
    </div>
  );
}
