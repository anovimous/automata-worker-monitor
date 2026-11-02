import * as React from "react";
import { Link, Outlet } from "@tanstack/react-router";
import { Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { isProblem } from "@/health";
import { cn } from "@/lib/cn";
import { useLiveState, useWorkers } from "@/live";

function useTheme(): [boolean, () => void] {
  const [dark, setDark] = React.useState(() => {
    const saved = localStorage.getItem("theme");
    return saved ? saved === "dark" : window.matchMedia?.("(prefers-color-scheme: dark)").matches === true;
  });
  React.useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
  }, [dark]);
  return [
    dark,
    () =>
      setDark((d) => {
        localStorage.setItem("theme", d ? "light" : "dark");
        return !d;
      }),
  ];
}

function LiveIndicator() {
  const state = useLiveState();
  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground" title="The live stream of the control plane">
      <span className={cn("h-2 w-2 rounded-full", state === "live" ? "bg-success" : "bg-warning animate-pulse-dot")} />
      {state === "live" ? "live" : state === "connecting" ? "connecting…" : "reconnecting…"}
    </span>
  );
}

// The tab title counts the workers with a problem: there are no notifications, the UI is where problems are seen.
function useTitle() {
  const { data } = useWorkers();
  const problems = data?.filter((w) => isProblem(w.health)).length ?? 0;
  React.useEffect(() => {
    document.title = (problems > 0 ? `(${problems}) ` : "") + "Automata workers";
  }, [problems]);
}

const navLink = "rounded-md px-2.5 py-1 text-sm text-muted-foreground hover:text-foreground";

export function Shell() {
  const [dark, toggle] = useTheme();
  useTitle();
  return (
    <div className="flex min-h-full flex-col">
      <header className="sticky top-0 z-40 border-b border-border bg-background/90 backdrop-blur">
        <div className="mx-auto flex h-12 max-w-[1600px] items-center gap-4 px-4">
          <Link to="/" className="text-sm font-semibold tracking-tight">
            Automata workers
          </Link>
          <nav className="flex items-center gap-1">
            <Link to="/" className={navLink} activeProps={{ className: "text-foreground bg-secondary" }} activeOptions={{ exact: true }}>
              Fleet
            </Link>
            <Link to="/hosts" className={navLink} activeProps={{ className: "text-foreground bg-secondary" }}>
              Hosts
            </Link>
            <Link to="/templates" className={navLink} activeProps={{ className: "text-foreground bg-secondary" }}>
              Templates
            </Link>
          </nav>
          <div className="ml-auto flex items-center gap-3">
            <LiveIndicator />
            <Button variant="ghost" size="icon-sm" onClick={toggle} aria-label="Toggle the theme">
              {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-[1600px] flex-1 px-4 py-5">
        <Outlet />
      </main>
    </div>
  );
}

export function PageTitle({ children, right }: { children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-center gap-3">
      <h1 className="text-lg font-semibold">{children}</h1>
      <div className="ml-auto flex flex-wrap items-center gap-2">{right}</div>
    </div>
  );
}

export function Section({ title, right, children, className }: { title: string; right?: React.ReactNode; children: React.ReactNode; className?: string }) {
  return (
    <section className={cn("space-y-2", className)}>
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-medium">{title}</h2>
        <div className="ml-auto flex items-center gap-2">{right}</div>
      </div>
      {children}
    </section>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
      {error instanceof Error ? error.message : String(error)}
    </div>
  );
}
