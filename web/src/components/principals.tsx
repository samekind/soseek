import { useState } from "react";
import {
  Check,
  Copy,
  KeyRound,
  Loader2,
  MoreHorizontal,
  Pencil,
  Plus,
  RotateCw,
  ShieldCheck,
  UserRound,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  ConfirmAction,
  CopyButton,
  Empty,
  ErrorNotice,
  LoadingRows,
  SectionHeading,
} from "@/components/shared";
import { api, money, type Principal } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

function PrincipalForm({
  principal,
  providers,
  onDone,
  onClose,
}: {
  principal?: Principal;
  providers: string[];
  onDone: (result: { principal: Principal; token?: string }) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(principal?.name ?? ""),
    [daily, setDaily] = useState(
      String((principal?.daily_micros ?? 5000000) / 1e6),
    );
  const [selected, setSelected] = useState(principal?.allowed_providers ?? []),
    [all, setAll] = useState(!principal?.allowed_providers?.length);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<string | null>(null);
  const choices = [...new Set([...providers, ...selected])];
  return (
    <form
      onSubmit={async (event) => {
        event.preventDefault();
        setError(null);
        if (!/^\d+(\.\d{1,6})?$/.test(daily) || Number(daily) <= 0) {
          setError("每日额度需要大于零，最多保留 6 位小数。");
          return;
        }
        if (!all && !selected.length) {
          setError("请至少选择一个服务，或允许所有服务。");
          return;
        }
        setBusy(true);
        try {
          const result = await api<{ principal: Principal; token?: string }>(
            principal
              ? `/principals/${encodeURIComponent(principal.id)}`
              : "/principals",
            {
              method: principal ? "PATCH" : "POST",
              body: {
                name: name.trim(),
                daily_usd: daily,
                providers: all ? [] : selected,
              },
            },
          );
          onDone(result);
        } catch (e) {
          setError(e instanceof Error ? e.message : "保存失败");
        } finally {
          setBusy(false);
        }
      }}
    >
      <DialogHeader>
        <DialogTitle>{principal ? "编辑访问权限" : "创建访问令牌"}</DialogTitle>
        <DialogDescription>
          {principal
            ? "调整名称、每日额度与可调用服务，历史任务仍保留。"
            : "为一位使用者创建独立身份，令牌只在创建后展示一次。"}
        </DialogDescription>
      </DialogHeader>
      <div className="space-y-5 py-6">
        <div className="space-y-2">
          <Label htmlFor="principal-name">使用者名称</Label>
          <Input
            id="principal-name"
            required
            maxLength={80}
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：研究助手"
            autoComplete="off"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="principal-daily">
            每日额度 <span className="font-normal text-zinc-400">USD</span>
          </Label>
          <Input
            id="principal-daily"
            required
            value={daily}
            onChange={(e) => setDaily(e.target.value)}
            inputMode="decimal"
          />
          <p className="text-xs leading-5 text-zinc-400">
            按 UTC 日计算，受服务总额度约束。预留费用也会占用额度。
          </p>
        </div>
        <fieldset className="space-y-3">
          <legend className="mb-3 text-sm font-medium">允许的服务</legend>
          <label className="flex cursor-pointer items-center gap-2.5 rounded-lg border px-3 py-3 text-sm">
            <Checkbox
              checked={all}
              onCheckedChange={(value) => setAll(value === true)}
            />
            所有已配置服务
          </label>
          {!all && (
            <div className="grid grid-cols-2 gap-2">
              {choices.map((provider) => (
                <label
                  key={provider}
                  className="flex cursor-pointer items-center gap-2.5 rounded-lg border px-3 py-2.5 text-sm capitalize"
                >
                  <Checkbox
                    checked={selected.includes(provider)}
                    onCheckedChange={(value) =>
                      setSelected((previous) =>
                        value
                          ? [...previous, provider]
                          : previous.filter((p) => p !== provider),
                      )
                    }
                  />
                  {provider}
                </label>
              ))}
              {!choices.length && (
                <p className="col-span-2 text-xs text-zinc-500">
                  当前没有可选择的上游服务。
                </p>
              )}
            </div>
          )}
        </fieldset>
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
          onClick={onClose}
        >
          取消
        </Button>
        <Button type="submit" disabled={busy || !name.trim()}>
          {busy && <Loader2 className="size-4 animate-spin" />}
          {principal ? "保存修改" : "创建令牌"}
        </Button>
      </DialogFooter>
    </form>
  );
}
export function PrincipalsPage({
  refresh,
  current,
  providers,
  notify,
}: {
  refresh: number;
  current: Principal;
  providers: string[];
  notify: (message: string) => void;
}) {
  const resource = useResource<{ items?: Principal[] }>("/principals", refresh);
  const [editing, setEditing] = useState<Principal | "new" | null>(null);
  const [action, setAction] = useState<{
    type: "rotate" | "revoke";
    principal: Principal;
  } | null>(null);
  const [issued, setIssued] = useState<{
      principal: Principal;
      token: string;
    } | null>(null),
    [saved, setSaved] = useState(false);
  const items = resource.data?.items ?? [];
  const complete = (result: { principal: Principal; token?: string }) => {
    setEditing(null);
    void resource.reload();
    if (result.token) {
      setSaved(false);
      setIssued({ principal: result.principal, token: result.token });
    } else notify("访问权限已更新");
  };
  return (
    <div className="space-y-5">
      <SectionHeading
        title="访问令牌"
        description="为每位使用者分配独立权限、用量额度和任务空间。"
        action={
          <Button onClick={() => setEditing("new")}>
            <Plus className="size-4" />
            创建令牌
          </Button>
        }
      />
      {resource.error && (
        <ErrorNotice error={resource.error} retry={resource.reload} />
      )}
      <section className="panel overflow-hidden">
        {resource.loading && !resource.data ? (
          <LoadingRows />
        ) : !items.length ? (
          <Empty
            title="还没有访问令牌"
            description="为使用者创建一个令牌，之后即可连接 CLI 使用搜索服务。"
            icon={<KeyRound className="size-5" />}
          >
            <Button onClick={() => setEditing("new")}>
              <Plus className="size-4" />
              创建第一个令牌
            </Button>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">使用者</TableHead>
                <TableHead>每日额度</TableHead>
                <TableHead>服务权限</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((principal) => (
                <TableRow key={principal.id}>
                  <TableCell className="py-4 pl-5">
                    <div className="flex items-center gap-3">
                      <div className="flex size-8 items-center justify-center rounded-full border bg-zinc-50 text-zinc-500">
                        {principal.admin ? (
                          <ShieldCheck className="size-4" />
                        ) : (
                          <UserRound className="size-4" />
                        )}
                      </div>
                      <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-medium">
                            {principal.name || "未命名"}
                          </span>
                          {principal.id === current.id && (
                            <Badge
                              variant="secondary"
                              className="text-[10px] font-normal"
                            >
                              当前账号
                            </Badge>
                          )}
                        </div>
                        <div className="mt-1 text-[11px] text-zinc-400">
                          {principal.admin ? "管理员" : "普通使用者"}
                        </div>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs tabular-nums">
                    {money(principal.daily_micros)}
                  </TableCell>
                  <TableCell>
                    <div className="flex max-w-64 flex-wrap gap-1.5">
                      {principal.allowed_providers?.length ? (
                        principal.allowed_providers.map((p) => (
                          <Badge
                            key={p}
                            variant="outline"
                            className="font-normal text-zinc-500"
                          >
                            {p}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-xs text-zinc-500">所有服务</span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={`font-normal ${principal.revoked ? "text-zinc-400" : "border-emerald-100 bg-emerald-50/70 text-emerald-700"}`}
                    >
                      {principal.revoked ? "已吊销" : "有效"}
                    </Badge>
                  </TableCell>
                  <TableCell className="pr-4">
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`管理 ${principal.name} 的令牌`}
                        >
                          <MoreHorizontal className="size-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-56">
                        <DropdownMenuLabel className="text-xs font-normal text-zinc-500">
                          {principal.name}
                        </DropdownMenuLabel>
                        <DropdownMenuItem
                          disabled={principal.revoked}
                          onSelect={() => setEditing(principal)}
                        >
                          <Pencil />
                          编辑权限
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={
                            principal.revoked || principal.id === current.id
                          }
                          onSelect={() =>
                            setAction({ type: "rotate", principal })
                          }
                        >
                          <RotateCw />
                          轮换令牌
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant="destructive"
                          disabled={
                            principal.revoked || principal.id === current.id
                          }
                          onSelect={() =>
                            setAction({ type: "revoke", principal })
                          }
                        >
                          <X />
                          吊销并取消未完成任务
                        </DropdownMenuItem>
                        {principal.id === current.id && (
                          <p className="px-2 py-2 text-[11px] leading-5 text-zinc-400">
                            当前管理员不能在此轮换或吊销自己的令牌，以免失去管理访问。
                          </p>
                        )}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>
      <p className="flex items-start gap-2 text-xs leading-5 text-zinc-400">
        <ShieldCheck className="mt-0.5 size-3.5 shrink-0" />
        普通访问令牌不能登录管理面板。管理员权限由服务器本地管理命令授予。
      </p>
      <Dialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
      >
        <DialogContent className="sm:max-w-lg">
          {editing && (
            <PrincipalForm
              key={editing === "new" ? "new" : editing.id}
              principal={editing === "new" ? undefined : editing}
              providers={providers}
              onDone={complete}
              onClose={() => setEditing(null)}
            />
          )}
        </DialogContent>
      </Dialog>
      <ConfirmAction
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) setAction(null);
        }}
        title={
          action?.type === "rotate"
            ? "轮换这个访问令牌？"
            : "吊销这个访问令牌？"
        }
        label={
          action?.type === "rotate" ? "轮换并生成新令牌" : "吊销并请求取消任务"
        }
        destructive={action?.type === "revoke"}
        description={
          action?.type === "rotate" ? (
            <>
              「{action.principal.name}
              」的旧令牌和所有浏览器会话会立即失效，需要使用新令牌重新登录。身份、权限和历史记录保留。
            </>
          ) : (
            <>
              「{action?.principal.name}
              」将无法再访问服务，已有浏览器会话立即失效，未完成任务将请求取消。已发生的上游调用可能仍然计费，历史记录保留。
            </>
          )
        }
        onConfirm={async () => {
          if (!action) return;
          if (action.type === "rotate") {
            const result = await api<{ principal: Principal; token: string }>(
              `/principals/${encodeURIComponent(action.principal.id)}/rotate`,
              { method: "POST", body: {} },
            );
            complete(result);
          } else {
            await api(
              `/principals/${encodeURIComponent(action.principal.id)}`,
              { method: "DELETE" },
            );
            await resource.reload();
            notify("令牌已吊销，未完成任务已请求取消");
          }
        }}
      />
      <Dialog
        open={issued !== null}
        onOpenChange={(open) => {
          if (!open && saved) setIssued(null);
        }}
      >
        <DialogContent className="sm:max-w-lg" showCloseButton={false}>
          <DialogHeader>
            <div className="mb-2 flex size-10 items-center justify-center rounded-full bg-emerald-50 text-emerald-600">
              <Check className="size-5" />
            </div>
            <DialogTitle>请保存新令牌</DialogTitle>
            <DialogDescription>
              这是「{issued?.principal?.name || "使用者"}
              」的新令牌，只展示这一次。关闭后无法再次查看，请通过私密方式交付给使用者。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="rounded-lg border bg-zinc-50 p-4">
              <code className="block break-all font-mono text-sm leading-6 select-all">
                {issued?.token}
              </code>
            </div>
            <CopyButton value={issued?.token ?? ""} label="复制令牌" />
            <label className="flex cursor-pointer items-center gap-2.5 text-sm">
              <Checkbox
                checked={saved}
                onCheckedChange={(value) => setSaved(value === true)}
              />
              我已将令牌保存到安全的位置
            </label>
          </div>
          <DialogFooter>
            <Button disabled={!saved} onClick={() => setIssued(null)}>
              完成
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
