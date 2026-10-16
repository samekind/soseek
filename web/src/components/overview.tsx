import {
  Activity,
  ArrowRight,
  CheckCircle2,
  Clock3,
  Layers3,
  Radio,
  Server,
  ShieldCheck,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Empty, ErrorNotice, SectionHeading } from "@/components/shared";
import { count, money, type Overview } from "@/lib/api";

export function OverviewPage({
  data,
  error,
  loading,
  refresh,
}: {
  data: Overview | null;
  error: string | null;
  loading: boolean;
  refresh: () => void;
}) {
  if (error && !data) return <ErrorNotice error={error} retry={refresh} />;
  if (!data)
    return (
      <div className="space-y-6">
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-32 rounded-xl" />
          ))}
        </div>
        <Skeleton className="h-72 rounded-xl" />
      </div>
    );
  const stats = data.statistics ?? {},
    channels = data.channels ?? [],
    available = channels.filter((x) => !x.disabled);
  const cards = [
    {
      label: "今日任务",
      value: count(stats.today_runs),
      hint: "按 UTC 日统计",
      icon: Layers3,
    },
    {
      label: "正在执行",
      value: count(stats.active_runs),
      hint: `${count(stats.queued_runs)} 个任务等待执行`,
      icon: Activity,
      active: (stats.active_runs ?? 0) > 0,
    },
    {
      label: "累计完成",
      value: count(stats.succeeded_runs),
      hint: `保留期内 · ${count(stats.partial_runs)} 个部分完成`,
      icon: CheckCircle2,
    },
    {
      label: "失败任务",
      value: count(stats.failed_runs),
      hint: "保留期内累计 · 查看任务了解原因",
      icon: Clock3,
    },
  ];
  return (
    <div className="space-y-7">
      {error && <ErrorNotice error={error} retry={refresh} />}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map(({ label, value, hint, icon: Icon, active }) => (
          <section key={label} className="panel p-5">
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-medium text-zinc-500">{label}</h2>
              <Icon
                className={`size-4 ${active ? "text-blue-600" : "text-zinc-400"}`}
              />
            </div>
            <div className="mt-4 text-[30px] leading-none font-semibold tracking-tight tabular-nums">
              {value}
            </div>
            <p className="mt-3 text-xs text-zinc-400">{hint}</p>
          </section>
        ))}
      </div>
      <div className="grid items-start gap-6 xl:grid-cols-[1.55fr_1fr]">
        <section className="panel overflow-hidden">
          <div className="border-b px-5 pt-5">
            <SectionHeading
              title="上游服务"
              description="当前部署的接入线路与可用操作"
              action={
                <Badge variant="outline" className="font-normal text-zinc-500">
                  {available.length} / {channels.length} 启用
                </Badge>
              }
            />
          </div>
          {channels.length ? (
            <div className="divide-y">
              {channels.map((channel) => (
                <div
                  key={channel.id}
                  className="flex items-start gap-3.5 px-5 py-4"
                >
                  <div className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-zinc-50 font-mono text-sm font-semibold uppercase text-zinc-600">
                    {channel.provider?.slice(0, 2) || "UP"}
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium capitalize">
                        {channel.provider}
                      </span>
                      <span className="text-xs text-zinc-400">
                        {channel.id}
                      </span>
                    </div>
                    <div className="mt-1.5 flex flex-wrap gap-1.5">
                      {(channel.operations ?? []).map((operation) => (
                        <code
                          key={operation}
                          className="rounded bg-zinc-100/80 px-1.5 py-0.5 text-[11px] text-zinc-500"
                        >
                          {operation}
                        </code>
                      ))}
                      {channel.model && (
                        <span className="text-[11px] leading-5 text-zinc-400">
                          {channel.model}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="mt-1 flex items-center gap-1.5 text-xs text-zinc-500">
                    <span
                      className={`size-1.5 rounded-full ${channel.disabled ? "bg-zinc-300" : "bg-emerald-500"}`}
                    />
                    {channel.disabled ? "已停用" : "已启用"}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <Empty
              title="还没有配置上游"
              description="在服务部署配置中添加线路后，这里会显示可用服务。"
              icon={<Radio className="size-5" />}
            />
          )}
          <div className="border-t bg-zinc-50/60 px-5 py-3">
            <Button
              asChild
              variant="link"
              className="h-auto p-0 text-xs text-zinc-600"
            >
              <a href="#credentials">
                管理上游密钥
                <ArrowRight className="size-3.5" />
              </a>
            </Button>
            <p className="mt-2 text-[11px] leading-5 text-zinc-400">
              启用表示配置状态，不代表已实时验证上游健康或余额。
            </p>
          </div>
        </section>
        <div className="space-y-6">
          <section className="panel p-5">
            <SectionHeading
              title="今日用量"
              description="分别查看已报告、估算与预留金额"
            />
            <div className="space-y-4">
              {[
                {
                  label: "上游报告",
                  value: data.usage?.reported_micros,
                  className: "bg-zinc-900",
                  help: "上游接口返回的美元用量",
                },
                {
                  label: "费用估算",
                  value: data.usage?.estimated_micros,
                  className: "bg-zinc-400",
                  help: "按配置估算，等待实际核对",
                },
                {
                  label: "仍在预留",
                  value: data.usage?.reserved_micros,
                  className: "bg-blue-500",
                  help: "预算占用，可能包含未知费用",
                },
              ].map((item) => (
                <div key={item.label}>
                  <div className="flex items-center justify-between">
                    <span className="flex items-center gap-2 text-sm text-zinc-600">
                      <span className={`size-2 rounded-sm ${item.className}`} />
                      {item.label}
                    </span>
                    <span className="font-mono text-[15px] font-medium tabular-nums">
                      {money(item.value)}
                    </span>
                  </div>
                  <p className="mt-1 pl-4 text-[11px] text-zinc-400">
                    {item.help}
                  </p>
                </div>
              ))}
            </div>
            <div className="mt-5 flex gap-2 rounded-lg border border-blue-100 bg-blue-50/60 p-3">
              <ShieldCheck className="mt-0.5 size-4 shrink-0 text-blue-600" />
              <p className="text-xs leading-5 text-blue-900/75">
                这里是用量与预算记录，不是最终账单。未知费用仍保留预留，不按零收费计算。
              </p>
            </div>
          </section>
          <section className="panel p-5">
            <div className="mb-4 flex items-center gap-2">
              <Server className="size-4 text-zinc-500" />
              <h2 className="text-sm font-medium">服务信息</h2>
            </div>
            <dl className="space-y-3 text-xs">
              <div className="flex justify-between gap-4">
                <dt className="text-zinc-500">版本</dt>
                <dd className="font-mono">{data.version || "—"}</dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt className="shrink-0 text-zinc-500">配置修订</dt>
                <dd className="truncate font-mono" title={data.revision}>
                  {data.revision?.slice(0, 16) || "—"}
                </dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt className="text-zinc-500">刷新方式</dt>
                <dd className="flex items-center gap-1.5 text-zinc-600">
                  <span
                    className={`size-1.5 rounded-full ${loading ? "animate-pulse bg-blue-500" : "bg-emerald-500"}`}
                  />
                  每 15 秒
                </dd>
              </div>
            </dl>
          </section>
        </div>
      </div>
    </div>
  );
}
