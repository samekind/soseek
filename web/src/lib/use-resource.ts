import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";

export function useResource<T>(
  endpoint: string,
  refresh = 0,
  enabled = true,
  interval?: number,
) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const request = useRef<AbortController | null>(null);
  const load = useCallback(async () => {
    if (!enabled) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError(null);
    try {
      const next = await api<T>(endpoint, { signal: controller.signal });
      if (!controller.signal.aborted) setData(next);
    } catch (error) {
      if (!controller.signal.aborted)
        setError(error instanceof Error ? error.message : "读取失败，请重试。");
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [endpoint, enabled]);
  useEffect(() => {
    setData(null);
    void load();
    return () => request.current?.abort();
  }, [load, refresh]);
  useEffect(() => {
    if (!interval || !enabled) return;
    const timer = window.setInterval(() => {
      if (!document.hidden) void load();
    }, interval);
    return () => window.clearInterval(timer);
  }, [load, interval, enabled]);
  return { data, error, loading, reload: load };
}
