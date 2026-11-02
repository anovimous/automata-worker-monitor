import { useQuery } from "@tanstack/react-query";
import { api } from "@/api";
import type { UIConfig } from "@/types";

export function useUIConfig() {
  return useQuery({ queryKey: ["config"], queryFn: () => api.get<UIConfig>("/config/ui"), staleTime: 60_000 });
}
