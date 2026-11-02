import * as React from "react";
import * as ToastPrimitive from "@radix-ui/react-toast";
import { X } from "lucide-react";
import { cn } from "@/lib/cn";

interface ToastItem {
  id: string;
  title?: string;
  description?: string;
  variant?: "default" | "success" | "destructive" | "warning";
  duration?: number;
}

type ToastFn = (t: Omit<ToastItem, "id">) => void;

const ToastCtx = React.createContext<{
  toast: ToastFn;
  toasts: ToastItem[];
  dismiss: (id: string) => void;
} | null>(null);

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = React.useState<ToastItem[]>([]);

  const toast: ToastFn = React.useCallback((t) => {
    const id = String(Date.now()) + Math.random().toString(36).slice(2, 6);
    setToasts((prev) => [...prev, { id, ...t }]);
  }, []);

  const dismiss = React.useCallback((id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  return (
    <ToastCtx.Provider value={{ toast, toasts, dismiss }}>
      <ToastPrimitive.Provider swipeDirection="right">
        {children}
        {toasts.map((t) => (
          <ToastPrimitive.Root
            key={t.id}
            duration={t.duration ?? 4000}
            onOpenChange={(open) => {
              if (!open) dismiss(t.id);
            }}
            className={cn(
              "group pointer-events-auto relative flex w-full max-w-sm items-center justify-between gap-3 rounded-md border p-3 pr-8 shadow-lg transition-all",
              "data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-right-full data-[state=closed]:fade-out-80",
              t.variant === "destructive" && "border-destructive/30 bg-destructive/10 text-foreground",
              t.variant === "success" && "border-success/30 bg-success/10 text-foreground",
              t.variant === "warning" && "border-warning/30 bg-warning/10 text-foreground",
              (!t.variant || t.variant === "default") && "border-border bg-popover text-popover-foreground",
            )}
          >
            <div className="grid gap-0.5 text-xs">
              {t.title && <div className="font-medium text-sm">{t.title}</div>}
              {t.description && <div className="text-muted-foreground">{t.description}</div>}
            </div>
            <ToastPrimitive.Close className="absolute right-2 top-2 rounded p-1 text-muted-foreground hover:text-foreground">
              <X className="h-3 w-3" />
            </ToastPrimitive.Close>
          </ToastPrimitive.Root>
        ))}
        <ToastPrimitive.Viewport className="fixed bottom-4 right-4 z-[100] flex max-h-screen w-full max-w-sm flex-col gap-2 outline-none" />
      </ToastPrimitive.Provider>
    </ToastCtx.Provider>
  );
}

export function useToast() {
  const ctx = React.useContext(ToastCtx);
  if (!ctx) throw new Error("useToast must be used inside <ToastProvider>");
  return ctx;
}
