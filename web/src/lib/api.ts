export type Principal = {
  id: string;
  name: string;
  daily_micros?: number;
  allowed_providers?: string[];
  admin?: boolean;
  revoked?: boolean;
};
export type Usage = {
  reported_micros?: number;
  estimated_micros?: number;
  reserved_micros?: number;
  unsettled_attempts?: number;
  credits?: number;
};
export type Channel = {
  id: string;
  provider: string;
  operations?: string[];
  model?: string;
  disabled?: boolean;
  quota_group?: string;
};
export type Overview = {
  version?: string;
  revision?: string;
  principal?: Principal;
  limits?: Record<string, unknown>;
  statistics?: {
    today_runs?: number;
    active_runs?: number;
    queued_runs?: number;
    succeeded_runs?: number;
    failed_runs?: number;
    partial_runs?: number;
  };
  usage?: Usage;
  channels?: Channel[];
};
export type Credential = {
  id: string;
  channels?: string[];
  providers?: string[];
  source?: "file" | "environment";
  configured?: boolean;
  disabled?: boolean;
  overridden?: boolean;
};
export type Run = {
  run_id: string;
  principal_id?: string;
  principal_name?: string;
  operation?: string;
  query?: string;
  url?: string;
  status?: string;
  created_at?: string;
  updated_at?: string;
  usage?: Usage;
  request?: {
    operation?: string;
    query?: string;
    url?: string;
    provider?: string;
    providers?: string[];
    mode?: string;
  };
  result?: {
    answer?: string;
    results?: Source[];
    warnings?: string[];
    gaps?: string[];
    stop_reason?: string;
    usage?: Usage;
    attempts?: Attempt[];
  };
  error?: { code?: string; message?: string };
};
export type Source = {
  source_id?: string;
  title?: string;
  url?: string;
  snippet?: string;
  content?: string;
  found_by?: string[];
};
export type Attempt = {
  id?: string;
  provider?: string;
  channel?: string;
  operation?: string;
  status?: string;
  cached?: boolean;
  error?: { code?: string; message?: string };
  usage?: Usage;
};
export type Audit = {
  id: string | number;
  actor_id?: string;
  actor_name?: string;
  action?: string;
  target?: string;
  created_at?: string;
};
export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}
export async function api<T>(
  endpoint: string,
  options: {
    method?: string;
    body?: unknown;
    signal?: AbortSignal;
    notifyUnauthorized?: boolean;
  } = {},
): Promise<T> {
  const method = options.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") headers["X-Soseek-Admin"] = "1";
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  let response: Response;
  try {
    response = await fetch(`/admin/api${endpoint}`, {
      method,
      headers,
      credentials: "same-origin",
      cache: "no-store",
      signal: options.signal,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError")
      throw error;
    throw new APIError(0, "暂时无法连接服务，请检查网络后重试。");
  }
  const text = await response.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      if (response.ok)
        throw new APIError(
          response.status,
          "服务返回了无法读取的数据，请刷新重试。",
        );
    }
  }
  if (!response.ok) {
    const error = (
      data as { error?: { message?: string; code?: string } } | null
    )?.error;
    if (response.status === 401 && options.notifyUnauthorized !== false)
      window.dispatchEvent(new Event("soseek:unauthorized"));
    const fallback =
      response.status === 403
        ? "当前令牌没有管理员权限。请使用已有的管理员令牌。"
        : response.status === 401
          ? "会话已过期，请重新登录。"
          : "操作未完成，请稍后重试。";
    throw new APIError(
      response.status,
      error?.message || fallback,
      error?.code,
    );
  }
  return data as T;
}
export function safeLink(value?: string) {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return ["http:", "https:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}
export function money(micros?: number) {
  if (micros === undefined || micros === null || !Number.isFinite(micros))
    return "—";
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  }).format(micros / 1e6);
}
export function dateTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}
export function count(value?: number) {
  return value === undefined
    ? "—"
    : new Intl.NumberFormat("zh-CN").format(value);
}
