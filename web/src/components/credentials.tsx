import { useState } from "react";
import {
  KeyRound,
  Loader2,
  MoreHorizontal,
  PauseCircle,
  RotateCcw,
  ShieldCheck,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
  LoadingRows,
  SectionHeading,
} from "@/components/shared";
import { api, type Credential } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

export function CredentialsPage({
  refresh,
  notify,
}: {
  refresh: number;
  notify: (message: string) => void;
}) {
  const resource = useResource<{ items?: Credential[] }>(
    "/credentials",
    refresh,
  );
  const [editing, setEditing] = useState<Credential | null>(null),
    [key, setKey] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<string | null>(null);
  const [action, setAction] = useState<{
    kind: "disable" | "restore";
    credential: Credential;
  } | null>(null);
  const items = resource.data?.items ?? [];
  const beginEdit = (credential: Credential) => {
    setKey("");
    setError(null);
    setEditing(credential);
  };
  return (
    <div className="space-y-5">
      <SectionHeading
        title="上游密钥"
        description="集中管理上游 API key；共用同一凭据的线路一起更新。"
      />
      <div className="flex items-start gap-3 rounded-lg border border-blue-100 bg-blue-50/50 px-4 py-3.5">
        <ShieldCheck className="mt-0.5 size-4 shrink-0 text-blue-600" />
        <p className="text-xs leading-6 text-blue-900/75">
          密钥只在填写时提交，已有值不会回传到浏览器。更新会覆盖该组当前使用的密钥；新增线路和服务能力仍通过部署配置管理。
        </p>
      </div>
      {resource.error && (
        <ErrorNotice error={resource.error} retry={resource.reload} />
      )}
      <section className="panel overflow-hidden">
        {resource.loading && !resource.data ? (
          <LoadingRows />
        ) : !items.length ? (
          <Empty
            title="没有配置密钥组"
            description="先在服务端配置上游线路和密钥来源，管理面板会显示可管理的密钥组。"
            icon={<KeyRound className="size-5" />}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">密钥组</TableHead>
                <TableHead>关联服务与线路</TableHead>
                <TableHead>来源</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((credential) => (
                <TableRow key={credential.id}>
                  <TableCell className="py-5 pl-5">
                    <div className="flex items-center gap-3">
                      <div className="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-zinc-50">
                        <KeyRound className="size-3.5 text-zinc-500" />
                      </div>
                      <div className="min-w-0">
                        <div
                          className="max-w-56 truncate font-mono text-xs font-medium"
                          title={credential.id}
                        >
                          {(credential.providers ?? []).join(" / ") || "上游"} 密钥
                        </div>
                        <div className="mt-1 text-[11px] text-zinc-400">
                          {(credential.channels ?? []).length} 条关联线路
                        </div>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1.5">
                      {(credential.providers ?? []).map((p) => (
                        <Badge
                          key={p}
                          variant="outline"
                          className="font-normal"
                        >
                          {p}
                        </Badge>
                      ))}
                    </div>
                    <p
                      className="mt-1.5 max-w-72 truncate text-[11px] text-zinc-400"
                      title={credential.channels?.join(", ")}
                    >
                      {credential.channels?.join(" · ") || "—"}
                    </p>
                  </TableCell>
                  <TableCell>
                    <div className="text-xs text-zinc-600">
                      {credential.overridden
                        ? "面板覆盖"
                        : credential.source === "environment"
                          ? "环境变量"
                          : credential.source === "file"
                            ? "部署文件"
                            : "未声明"}
                    </div>
                    {credential.overridden && (
                      <div className="mt-1 text-[11px] text-zinc-400">
                        可恢复部署配置
                      </div>
                    )}
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={`font-normal ${credential.disabled ? "bg-zinc-100 text-zinc-500" : credential.configured ? "border-emerald-100 bg-emerald-50/70 text-emerald-700" : "border-amber-100 bg-amber-50 text-amber-700"}`}
                    >
                      {credential.disabled
                        ? "已停用"
                        : credential.configured
                          ? "已配置"
                          : "缺少密钥"}
                    </Badge>
                  </TableCell>
                  <TableCell className="pr-4">
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`管理密钥组 ${credential.id}`}
                        >
                          <MoreHorizontal className="size-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem
                          onSelect={() => beginEdit(credential)}
                        >
                          <KeyRound />
                          更新密钥
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={credential.disabled}
                          onSelect={() =>
                            setAction({ kind: "disable", credential })
                          }
                        >
                          <PauseCircle />
                          停用密钥组
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          disabled={
                            !credential.overridden && !credential.disabled
                          }
                          onSelect={() =>
                            setAction({ kind: "restore", credential })
                          }
                        >
                          <RotateCcw />
                          恢复部署配置
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>
      <Dialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open && !busy) {
            setEditing(null);
            setKey("");
          }
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              if (!editing || !key.trim()) return;
              setBusy(true);
              setError(null);
              try {
                await api(`/credentials/${encodeURIComponent(editing.id)}`, {
                  method: "PUT",
                  body: { key: key.trim() },
                });
                setEditing(null);
                setKey("");
                await resource.reload();
                notify("密钥已更新，后续调用将使用新配置");
              } catch (e) {
                setError(e instanceof Error ? e.message : "更新失败");
              } finally {
                setBusy(false);
              }
            }}
          >
            <DialogHeader>
              <DialogTitle>更新上游密钥</DialogTitle>
              <DialogDescription>
                将新密钥用于「{editing?.id}
                」关联的所有线路。请确认它属于对应服务或共享接入账户。
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 py-6">
              <div className="space-y-2">
                <Label htmlFor="upstream-key">新的 API key</Label>
                <Input
                  id="upstream-key"
                  type="password"
                  required
                  autoFocus
                  autoComplete="new-password"
                  spellCheck={false}
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                  placeholder="粘贴新的密钥"
                />
              </div>
              <p className="text-xs leading-5 text-zinc-500">
                关联线路：{editing?.channels?.join("、") || "无"}
                。更新会恢复这组密钥的使用状态；真实可用性仍取决于上游账号与额度。
              </p>
              {error && (
                <p
                  role="alert"
                  className="rounded-lg bg-red-50 p-3 text-sm text-red-700"
                >
                  {error}
                </p>
              )}
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => {
                  setEditing(null);
                  setKey("");
                }}
              >
                取消
              </Button>
              <Button type="submit" disabled={busy || !key.trim()}>
                {busy && <Loader2 className="size-4 animate-spin" />}保存密钥
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <ConfirmAction
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) setAction(null);
        }}
        title={action?.kind === "disable" ? "停用这组密钥？" : "恢复部署配置？"}
        label={action?.kind === "disable" ? "停用密钥组" : "恢复部署配置"}
        destructive={action?.kind === "disable"}
        description={
          action?.kind === "disable" ? (
            <>
              「{action.credential.id}
              」关联线路的后续调用将无法使用这组密钥。已发出的请求可能仍会完成并计费。此操作不会在上游平台吊销密钥。
            </>
          ) : (
            <>
              将移除「{action?.credential.id}
              」在面板中保存的覆盖值和停用状态，重新使用部署文件或环境变量中的配置。不会删除上游账户或部署密钥文件；若原配置没有密钥，关联线路将不可用。
            </>
          )
        }
        onConfirm={async () => {
          if (!action) return;
          const id = encodeURIComponent(action.credential.id);
          if (action.kind === "disable")
            await api(`/credentials/${id}/disable`, {
              method: "POST",
              body: {},
            });
          else await api(`/credentials/${id}`, { method: "DELETE" });
          await resource.reload();
          notify(action.kind === "disable" ? "密钥组已停用" : "已恢复部署配置");
        }}
      />
    </div>
  );
}
