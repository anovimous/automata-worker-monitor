import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { TooltipProvider } from "@/components/ui/primitives";
import { ToastProvider } from "@/components/ui/toast";
import { LiveProvider } from "@/live";
import { router } from "@/router";
import "@/styles/globals.css";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <LiveProvider>
        <TooltipProvider delayDuration={200}>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </TooltipProvider>
      </LiveProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
