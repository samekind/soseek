import { useState, type ReactNode } from "react";
import {
  AlertCircle,
  ArrowUpRight,
  Check,
  CheckCircle2,
  Circle,
  Clock3,
  Copy,
  Loader2,
  RefreshCw,
  SearchX,
  XCircle,
} from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog";
import { money, safeLink, type Usage } from "@/lib/api";

const states: Record<
  string,
  { label: string; color: string; icon: typeof Circle }
> = {
  queued: {
    label: "排队中",
    color: "text-zinc-600 bg-zinc-100 border-zinc-200",
    icon: Clock3,
  },
  running: {
    label: "执行中",
    color: "text-blue-700 bg-blue-50 border-blue-100",
    icon: Loader2,
  },
  succeeded: {
    label: "已完成",
    color: "text-emerald-700 bg-emerald-50/80 border-emerald-100",
    icon: CheckCircle2,
  },
  partial: {
    label: "部分完成",
    color: "text-amber-700 bg-amber-50 border-amber-100",
    icon: AlertCircle,
  },
  failed: {
    label: "失败",
    color: "text-red-700 bg-red-50 border-red-100",
    icon: XCircle,
  },
  cancelled: {
    label: "已取消",
    color: "text-zinc-500 bg-zinc-50 border-zinc-200",
    icon: Circle,
  },
};
export function Status({ status }: { status?: string }) {
  const info = states[status ?? ""] ?? {
    label: status || "未知",
    color: "text-zinc-500 bg-zinc-50 border-zinc-200",
    icon: Circle,
  };
  const Icon = info.icon;
  return (
    <Badge
      variant="outline"
      className={`gap-1.5 px-2 py-0.5 font-normal ${info.color}`}
    >
      <Icon
        className={`size-3 ${status === "running" ? "animate-spin motion-reduce:animate-none" : ""}`}
      />
      {info.label}
    </Badge>
  );
}
export function ErrorNotice({
  error,
  retry,
}: {
  error: string;
  retry?: () => void;
}) {
  return (
    <Alert variant="destructive" className="bg-red-50/50">
      <AlertCircle className="size-4" />
      <AlertTitle>暂时没有完成</AlertTitle>
      <AlertDescription className="flex w-full flex-wrap items-center justify-between gap-3">
        <span className="break-words">{error}</span>
        {retry && (
          <Button size="sm" variant="outline" onClick={retry}>
            <RefreshCw className="size-3.5" />
            重试
          </Button>
        )}
      </AlertDescription>
    </Alert>
  );
}
export function Empty({
  title,
  description,
  icon = <SearchX className="size-5" />,
  children,
}: {
  title: string;
  description: string;
  icon?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex min-h-64 flex-col items-center justify-center px-6 py-14 text-center">
      <div className="mb-4 rounded-xl border bg-white p-3 text-zinc-400 shadow-xs">
        {icon}
      </div>
      <h3 className="text-sm font-medium text-zinc-900">{title}</h3>
      <p className="mt-2 max-w-md text-sm leading-6 text-zinc-500">
        {description}
      </p>
      {children && <div className="mt-5">{children}</div>}
    </div>
  );
}
export function LoadingRows({ rows = 4 }: { rows?: number }) {
  return (
    <div role="status" aria-label="正在加载" className="space-y-5 p-6">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex items-center gap-4">
          <Skeleton className="size-9 rounded-lg" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3 w-2/5" />
            <Skeleton className="h-3 w-3/5" />
          </div>
          <Skeleton className="h-6 w-16" />
        </div>
      ))}
    </div>
  );
}
export function SectionHeading({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 className="text-base font-semibold tracking-tight">{title}</h2>
        {description && (
          <p className="mt-1 text-sm leading-6 text-zinc-500">{description}</p>
        )}
      </div>
      {action}
    </div>
  );
}
export function UsageNumbers({ usage }: { usage?: Usage }) {
  return (
    <div className="grid grid-cols-3 gap-3 rounded-lg border bg-zinc-50/70 px-4 py-3">
      {[
        ["上游报告", usage?.reported_micros],
        ["估算", usage?.estimated_micros],
        ["仍在预留", usage?.reserved_micros],
      ].map(([label, value]) => (
        <div key={String(label)}>
          <div className="text-xs text-zinc-500">{label}</div>
          <div className="mt-1 font-mono text-sm tabular-nums text-zinc-900">
            {money(value as number | undefined)}
          </div>
        </div>
      ))}
    </div>
  );
}
export function CopyButton({
  value,
  label = "复制",
  onCopied,
}: {
  value: string;
  label?: string;
  onCopied?: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const [failed, setFailed] = useState(false);
  return (
    <div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value);
            setCopied(true);
            setFailed(false);
            onCopied?.();
            window.setTimeout(() => setCopied(false), 2000);
          } catch {
            setFailed(true);
          }
        }}
      >
        {copied ? (
          <Check className="size-3.5" />
        ) : (
          <Copy className="size-3.5" />
        )}
        {copied ? "已复制" : label}
      </Button>
      {failed && (
        <p role="alert" className="mt-2 text-xs text-red-600">
          无法访问剪贴板，请手动选择并复制。
        </p>
      )}
    </div>
  );
}
export function ExternalLink({
  href,
  children,
  className = "",
}: {
  href?: string;
  children: ReactNode;
  className?: string;
}) {
  const safe = safeLink(href);
  return safe ? (
    <a
      href={safe}
      target="_blank"
      rel="noreferrer noopener"
      className={`inline-flex items-center gap-1.5 text-blue-600 hover:text-blue-800 hover:underline ${className}`}
    >
      {children}
      <ArrowUpRight className="size-3.5 shrink-0" />
    </a>
  ) : (
    <span className={className}>{children}</span>
  );
}
export function ConfirmAction({
  open,
  onOpenChange,
  title,
  description,
  label,
  onConfirm,
  destructive = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  label: string;
  onConfirm: () => Promise<void>;
  destructive?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <AlertDialog
      open={open}
      onOpenChange={(value) => {
        if (!busy) {
          setError(null);
          onOpenChange(value);
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="text-sm leading-6 text-zinc-500">{description}</div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p
            role="alert"
            className="rounded-md bg-red-50 p-3 text-sm text-red-700"
          >
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>返回</AlertDialogCancel>
          <Button
            variant={destructive ? "destructive" : "default"}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setError(null);
              try {
                await onConfirm();
                onOpenChange(false);
              } catch (e) {
                setError(e instanceof Error ? e.message : "操作失败");
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy && <Loader2 className="size-4 animate-spin" />}
            {label}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
