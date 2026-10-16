import { useEffect, useState } from "react";
import {
  ArrowRight,
  Clock3,
  ExternalLink,
  FileText,
  Loader2,
  Search,
  Square,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  ConfirmAction,
  Empty,
  ErrorNotice,
  ExternalLink as SourceLink,
  LoadingRows,
  SectionHeading,
  Status,
  UsageNumbers,
} from "@/components/shared";
import { api, dateTime, money, type Run } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type RunSummary = Run & { expired?: boolean; query_truncated?: boolean };
export function RunDetails({
  id,
  open,
  onOpenChange,
}: {
  id: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { data, error, loading, reload } = useResource<Run>(
    `/runs/${encodeURIComponent(id)}`,
    0,
    open && !!id,
  );
  const result = data?.result;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88dvh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>任务详情</DialogTitle>
          <DialogDescription className="break-all font-mono text-[11px]">
            {id}
          </DialogDescription>
        </DialogHeader>
        {loading && !data ? (
          <LoadingRows rows={3} />
        ) : error ? (
          <ErrorNotice error={error} retry={reload} />
        ) : data ? (
          <div className="space-y-5 py-2">
            <div className="flex flex-wrap items-center gap-2">
              <Status status={data.status} />
              <Badge variant="outline" className="font-mono font-normal">
                {data.request?.operation || data.operation || "任务"}
              </Badge>
              <span className="text-xs text-zinc-400">
                {dateTime(data.created_at)}
              </span>
            </div>
            <div className="rounded-lg border bg-zinc-50/50 p-4">
              <p className="mb-2 text-[11px] font-medium text-zinc-400">
                任务输入
              </p>
              <p className="text-sm leading-6 break-words whitespace-pre-wrap">
                {data.request?.query ||
                  data.request?.url ||
                  data.query ||
                  data.url ||
                  "此任务没有文本输入。"}
              </p>
            </div>
            {data.error && (
              <ErrorNotice
                error={data.error.message || data.error.code || "任务执行失败"}
              />
            )}
            <UsageNumbers usage={result?.usage || data.usage} />
            {(result?.usage?.unsettled_attempts ?? 0) > 0 && (
              <p className="text-xs leading-5 text-amber-700">
                {result?.usage?.unsettled_attempts}{" "}
                次调用的最终费用尚未确定，相关预留仍保留。
              </p>
            )}
            {!!result?.warnings?.length && (
              <section className="rounded-lg border border-amber-100 bg-amber-50/60 p-4">
                <h3 className="mb-2 text-xs font-medium text-amber-900">
                  执行提示
                </h3>
                <ul className="list-disc space-y-1 pl-4 text-xs leading-6 text-amber-800">
                  {result.warnings.map((warning, i) => (
                    <li key={i}>{warning}</li>
                  ))}
                </ul>
              </section>
            )}
            {result?.answer && (
              <section>
                <h3 className="mb-3 text-sm font-semibold">回答</h3>
                <div className="rounded-lg border bg-white p-4 text-sm leading-7 break-words whitespace-pre-wrap">
                  {result.answer}
                </div>
              </section>
            )}
            {!!result?.results?.length && (
              <section>
                <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold">
                  结果来源{" "}
                  <span className="text-xs font-normal text-zinc-400">
                    {result.results.length}
                  </span>
                </h3>
                <div className="space-y-3">
                  {result.results.map((source, i) => (
                    <article
                      key={`${source.url}-${i}`}
                      className="rounded-lg border p-4"
                    >
                      <div className="flex items-start gap-2">
                        <span className="mt-0.5 rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-[10px] text-zinc-500">
                          {source.source_id || `S${i + 1}`}
                        </span>
                        <SourceLink
                          href={source.url}
                          className="min-w-0 text-sm font-medium break-words"
                        >
                          {source.title || source.url || "未命名来源"}
                        </SourceLink>
                      </div>
                      <p className="mt-2 truncate text-[11px] text-zinc-400">
                        {source.url}
                      </p>
                      {source.snippet && (
                        <p className="mt-2 text-xs leading-6 break-words whitespace-pre-wrap text-zinc-600">
                          {source.snippet}
                        </p>
                      )}
                      {source.content && (
                        <details className="mt-3">
                          <summary className="cursor-pointer text-xs text-zinc-500 hover:text-zinc-900">
                            查看正文
                          </summary>
                          <pre className="mt-3 max-h-72 overflow-y-auto rounded-md bg-zinc-50 p-3 font-sans text-xs leading-6 break-words whitespace-pre-wrap">
                            {source.content}
                          </pre>
                        </details>
                      )}
                    </article>
                  ))}
                </div>
              </section>
            )}
            {!!result?.gaps?.length && (
              <section>
                <h3 className="mb-2 text-sm font-semibold">尚未解决</h3>
                <ul className="list-disc space-y-1 pl-5 text-xs leading-6 text-zinc-600">
                  {result.gaps.map((gap, i) => (
                    <li key={i}>{gap}</li>
                  ))}
                </ul>
              </section>
            )}
            {!!result?.attempts?.length && (
              <section>
                <h3 className="mb-3 text-sm font-semibold">上游调用</h3>
                <div className="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>服务 / 操作</TableHead>
                        <TableHead>状态</TableHead>
                        <TableHead>记录</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {result.attempts.map((attempt, i) => (
                        <TableRow key={attempt.id || i}>
                          <TableCell>
                            <div className="text-xs font-medium">
                              {attempt.provider || "—"}{" "}
                              <span className="font-normal text-zinc-400">
                                / {attempt.operation}
                              </span>
                            </div>
                            <p className="mt-1 text-[10px] text-zinc-400">
                              {attempt.channel}
                            </p>
                          </TableCell>
                          <TableCell>
                            <Status status={attempt.status} />
                          </TableCell>
                          <TableCell className="max-w-64 whitespace-normal">
                            <p className="text-xs text-zinc-500">
                              {attempt.cached
                                ? "缓存命中"
                                : `上游报告 ${money(attempt.usage?.reported_micros)}`}
                            </p>
                            {attempt.error && (
                              <p className="mt-1 text-[11px] leading-5 text-red-600">
                                {attempt.error.message || attempt.error.code}
                              </p>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </section>
            )}
            <details className="rounded-lg border">
              <summary className="cursor-pointer px-4 py-3 text-xs font-medium text-zinc-600">
                原始任务 JSON
              </summary>
              <pre className="max-h-80 overflow-auto border-t bg-zinc-50 p-4 text-[11px] leading-5">
                {JSON.stringify(data, null, 2)}
              </pre>
            </details>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
export function RunsPage({
  refresh,
  notify,
}: {
  refresh: number;
  notify: (message: string) => void;
}) {
  const [cursor, setCursor] = useState(""),
    [items, setItems] = useState<RunSummary[]>([]),
    [selected, setSelected] = useState<RunSummary | null>(null),
    [cancelling, setCancelling] = useState<RunSummary | null>(null);
  const resource = useResource<{ items?: RunSummary[]; next_cursor?: string }>(
    `/runs?limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
    refresh,
  );
  useEffect(() => {
    setCursor("");
    setItems([]);
  }, [refresh]);
  useEffect(() => {
    if (resource.data)
      setItems((previous) =>
        cursor
          ? [
              ...previous,
              ...(resource.data?.items ?? []).filter(
                (next) => !previous.some((item) => item.run_id === next.run_id),
              ),
            ]
          : (resource.data?.items ?? []),
      );
  }, [resource.data, cursor]);
  const reload = () => {
    setCursor("");
    void resource.reload();
  };
  return (
    <div className="space-y-5">
      <SectionHeading
        title="任务记录"
        description="查看所有使用者的任务状态、结果与调用过程。"
        action={
          <Badge
            variant="outline"
            className="h-8 px-3 font-normal text-zinc-500"
          >
            <Clock3 className="mr-1 size-3.5" />
            按创建时间倒序
          </Badge>
        }
      />
      {resource.error && (
        <ErrorNotice error={resource.error} retry={resource.reload} />
      )}
      <section className="panel overflow-hidden">
        {resource.loading && !items.length ? (
          <LoadingRows rows={5} />
        ) : !items.length ? (
          <Empty
            title="还没有任务记录"
            description="使用 CLI 提交搜索或研究任务后，会在这里显示执行进度与结果。"
            icon={<FileText className="size-5" />}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">任务</TableHead>
                <TableHead>使用者</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>创建时间</TableHead>
                <TableHead className="w-20">
                  <span className="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((run) => (
                <TableRow key={run.run_id}>
                  <TableCell className="max-w-[360px] py-4 pl-5">
                    <button
                      className="group block w-full text-left outline-none focus-visible:rounded focus-visible:ring-2 focus-visible:ring-blue-500"
                      onClick={() => setSelected(run)}
                    >
                      <div className="flex items-center gap-2">
                        <span className="rounded border bg-zinc-50 px-1.5 py-0.5 font-mono text-[10px] text-zinc-500">
                          {run.operation || run.request?.operation || "任务"}
                        </span>
                        {run.expired && (
                          <span className="text-[10px] text-zinc-400">
                            内容已过期
                          </span>
                        )}
                      </div>
                      <p className="mt-2 truncate text-sm font-medium group-hover:text-blue-600">
                        {run.query ||
                          run.url ||
                          run.request?.query ||
                          "查看任务详情"}
                        {run.query_truncated ? "…" : ""}
                      </p>
                      <p className="mt-1 truncate font-mono text-[10px] text-zinc-400">
                        {run.run_id}
                      </p>
                    </button>
                  </TableCell>
                  <TableCell className="text-xs text-zinc-600">
                    {run.principal_name || run.principal_id || "—"}
                  </TableCell>
                  <TableCell>
                    <Status status={run.status} />
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums text-zinc-500">
                    {dateTime(run.created_at)}
                  </TableCell>
                  <TableCell className="pr-4">
                    {["queued", "running"].includes(run.status ?? "") ? (
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="请求取消任务"
                        onClick={() => setCancelling(run)}
                      >
                        <Square className="size-3.5 text-zinc-400" />
                      </Button>
                    ) : (
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="查看任务详情"
                        onClick={() => setSelected(run)}
                      >
                        <ArrowRight className="size-4 text-zinc-400" />
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {resource.data?.next_cursor && (
          <div className="flex justify-center border-t p-3">
            <Button
              variant="ghost"
              size="sm"
              disabled={resource.loading}
              onClick={() => setCursor(resource.data?.next_cursor || "")}
            >
              {resource.loading && (
                <Loader2 className="size-3.5 animate-spin" />
              )}
              加载更早的任务
            </Button>
          </div>
        )}
      </section>
      {selected && (
        <RunDetails
          id={selected.run_id}
          open
          onOpenChange={(open) => {
            if (!open) setSelected(null);
          }}
        />
      )}
      <ConfirmAction
        open={cancelling !== null}
        onOpenChange={(open) => {
          if (!open) setCancelling(null);
        }}
        title="请求取消这个任务？"
        description="服务将停止后续步骤，并尽力中断已经发出的请求。上游可能已经产生费用，取消不保证退还用量。已完成的任务会保留结果。"
        label="请求取消任务"
        destructive
        onConfirm={async () => {
          if (!cancelling) return;
          await api(`/runs/${encodeURIComponent(cancelling.run_id)}/cancel`, {
            method: "POST",
            body: {},
          });
          reload();
          notify("已发送取消请求，请刷新查看最终状态");
        }}
      />
    </div>
  );
}
