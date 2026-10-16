import { useEffect, useState } from "react";
import { ClipboardList, Loader2, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Empty,
  ErrorNotice,
  LoadingRows,
  SectionHeading,
} from "@/components/shared";
import { dateTime, type Audit } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const labels: Record<string, string> = {
  "admin.granted": "授予管理员权限",
  "session.created": "登录管理面板",
  "session.deleted": "退出管理面板",
  "principal.created": "创建访问令牌",
  "principal.updated": "修改使用者权限",
  "principal.token_rotated": "轮换访问令牌",
  "principal.revoked": "吊销访问令牌",
  "credential.update": "更新上游密钥",
  "credential.disable": "停用密钥组",
  "credential.reset": "恢复部署密钥配置",
  "credential.update_requested": "请求更新上游密钥",
  "credential.disable_requested": "请求停用密钥组",
  "credential.reset_requested": "请求恢复部署配置",
  "run.cancel_requested": "请求取消任务",
};
export function AuditPage({ refresh }: { refresh: number }) {
  const [cursor, setCursor] = useState(""),
    [items, setItems] = useState<Audit[]>([]);
  const resource = useResource<{ items?: Audit[]; next_cursor?: string }>(
    `/audit?limit=30${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
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
                (next) => !previous.some((item) => item.id === next.id),
              ),
            ]
          : (resource.data?.items ?? []),
      );
  }, [resource.data, cursor]);
  return (
    <div className="space-y-5">
      <SectionHeading
        title="操作审计"
        description="记录管理员对访问权限、密钥和任务的操作，不记录密钥明文。"
        action={
          <Badge
            variant="outline"
            className="h-8 px-3 font-normal text-zinc-500"
          >
            <ShieldCheck className="mr-1 size-3.5" />
            只读记录
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
            title="暂无操作记录"
            description="管理员登录以及修改权限、密钥等操作，会显示在这里。"
            icon={<ClipboardList className="size-5" />}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">操作</TableHead>
                <TableHead>操作者</TableHead>
                <TableHead>对象</TableHead>
                <TableHead className="pr-5">时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={item.id}>
                  <TableCell className="py-4 pl-5">
                    <span className="text-sm">
                      {labels[item.action ?? ""] || item.action || "管理操作"}
                    </span>
                    {item.action?.endsWith("_requested") && (
                      <p className="mt-1 text-[10px] text-zinc-400">
                        已记录请求，完成情况见后续记录
                      </p>
                    )}
                  </TableCell>
                  <TableCell className="text-xs text-zinc-600">
                    {item.actor_name === "local-cli"
                      ? "服务器本地管理"
                      : item.actor_name || item.actor_id || "—"}
                  </TableCell>
                  <TableCell
                    className="max-w-64 truncate font-mono text-[11px] text-zinc-500"
                    title={item.target}
                  >
                    {item.target || "—"}
                  </TableCell>
                  <TableCell className="pr-5 text-xs whitespace-nowrap tabular-nums text-zinc-500">
                    {dateTime(item.created_at)}
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
              加载更早的记录
            </Button>
          </div>
        )}
      </section>
    </div>
  );
}
