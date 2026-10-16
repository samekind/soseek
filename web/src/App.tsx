import { useCallback, useEffect, useState } from "react";
import {
  Activity,
  ArrowRight,
  CheckCircle2,
  ChevronDown,
  ClipboardList,
  Command,
  KeyRound,
  LayoutDashboard,
  Loader2,
  LogOut,
  Menu,
  RefreshCw,
  ScanSearch,
  ShieldCheck,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { OverviewPage } from "@/components/overview";
import { PrincipalsPage } from "@/components/principals";
import { CredentialsPage } from "@/components/credentials";
import { RunsPage } from "@/components/runs";
import { AuditPage } from "@/components/audit";
import { api, APIError, type Overview, type Principal } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const navigation = [
  { id: "overview", label: "概览", icon: LayoutDashboard },
  { id: "runs", label: "任务记录", icon: Activity },
  { id: "principals", label: "访问令牌", icon: KeyRound },
  { id: "credentials", label: "上游密钥", icon: SlidersHorizontal },
  { id: "audit", label: "操作审计", icon: ClipboardList },
];
function currentPage() {
  const hash = window.location.hash.slice(1);
  return navigation.some((x) => x.id === hash) ? hash : "overview";
}
function Brand({ large = false }: { large?: boolean }) {
  return (
    <div className="flex items-center gap-2.5">
      <div
        className={`flex items-center justify-center rounded-[9px] bg-zinc-900 text-white shadow-xs ${large ? "size-10" : "size-8"}`}
      >
        <ScanSearch
          className={large ? "size-5" : "size-[17px]"}
          strokeWidth={1.8}
        />
      </div>
      <span
        className={`font-semibold tracking-[-0.055em] ${large ? "text-[27px]" : "text-[23px]"}`}
      >
        soseek<span className="text-blue-500">.</span>
      </span>
    </div>
  );
}
function Login({
  onLogin,
  sessionError,
}: {
  onLogin: (principal: Principal) => void;
  sessionError: string | null;
}) {
  const [token, setToken] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<string | null>(sessionError);
  return (
    <main className="flex min-h-dvh flex-col bg-[#fafafa]">
      <header className="flex items-center justify-between border-b border-zinc-200/70 px-6 py-5 sm:px-10">
        <Brand />
        <Badge
          variant="outline"
          className="gap-1.5 bg-white font-normal text-zinc-500"
        >
          <ShieldCheck className="size-3.5" />
          管理中心
        </Badge>
      </header>
      <div className="mx-auto grid w-full max-w-5xl flex-1 items-center gap-14 px-6 py-14 lg:grid-cols-2 lg:gap-24">
        <div className="hidden lg:block">
          <div className="mb-5 inline-flex items-center gap-2 text-[11px] font-medium tracking-[0.15em] text-zinc-400">
            <span className="size-1.5 rounded-full bg-blue-500" />
            SOSEEK ADMIN
          </div>
          <h1 className="text-[40px] leading-[1.35] font-semibold tracking-tight">
            让搜索服务，
            <br />
            有序运行。
          </h1>
          <p className="mt-5 max-w-sm text-sm leading-7 text-zinc-500">
            在一个地方查看任务、分配访问权限、管理上游密钥。保持共享服务清晰、可控。
          </p>
          <div className="mt-10 space-y-4">
            {[
              ["任务执行", "从状态到来源，追踪每一次调用。"],
              ["访问与用量", "独立身份、各自额度，保留费用边界。"],
              ["配置管理", "统一维护密钥，重要操作留下记录。"],
            ].map(([title, description], i) => (
              <div key={title} className="flex gap-3">
                <span className="mt-0.5 font-mono text-[10px] text-zinc-300">
                  0{i + 1}
                </span>
                <div>
                  <p className="text-xs font-medium text-zinc-700">{title}</p>
                  <p className="mt-1 text-xs leading-5 text-zinc-400">
                    {description}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>
        <section className="panel mx-auto w-full max-w-[420px] p-7 sm:p-8">
          <div className="mb-7">
            <div className="mb-5 flex size-10 items-center justify-center rounded-xl border bg-zinc-50 text-zinc-700">
              <KeyRound className="size-[18px]" />
            </div>
            <h2 className="text-xl font-semibold tracking-tight">
              登录管理中心
            </h2>
            <p className="mt-2 text-sm leading-6 text-zinc-500">
              使用已有的管理员令牌继续。
            </p>
          </div>
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              setBusy(true);
              setError(null);
              try {
                const data = await api<{ principal: Principal }>("/session", {
                  method: "POST",
                  body: { token: token.trim() },
                });
                setToken("");
                onLogin(data.principal);
              } catch (e) {
                setError(
                  e instanceof APIError && e.status === 403
                    ? "这个令牌没有管理员权限。请向服务管理员获取已授权的管理员令牌。"
                    : e instanceof Error
                      ? e.message
                      : "登录失败，请重试。",
                );
              } finally {
                setBusy(false);
              }
            }}
          >
            <div className="space-y-2">
              <Label htmlFor="admin-token">管理员令牌</Label>
              <Input
                id="admin-token"
                type="password"
                autoComplete="current-password"
                autoFocus
                required
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="粘贴管理员访问令牌"
                className="h-10"
                spellCheck={false}
              />
            </div>
            {error && (
              <p
                role="alert"
                className="mt-4 rounded-lg bg-red-50 px-3 py-2.5 text-xs leading-6 text-red-700"
              >
                {error}
              </p>
            )}
            <Button
              className="mt-5 h-10 w-full"
              type="submit"
              disabled={busy || !token.trim()}
            >
              {busy ? <Loader2 className="size-4 animate-spin" /> : null}
              {busy ? "正在验证" : "进入管理中心"}
              {!busy && <ArrowRight className="ml-auto size-4" />}
            </Button>
          </form>
          <div className="mt-6 border-t pt-5">
            <p className="text-xs leading-6 text-zinc-400">
              普通调用令牌无法访问管理功能。登录后使用安全会话，令牌不会保存在浏览器本地存储中。
            </p>
          </div>
        </section>
      </div>
      <footer className="px-6 py-6 text-center text-[11px] text-zinc-400">
        soseek · 搜索与研究服务管理
      </footer>
    </main>
  );
}
export default function App() {
  const [principal, setPrincipal] = useState<Principal | null>(null),
    [checking, setChecking] = useState(true),
    [sessionError, setSessionError] = useState<string | null>(null);
  const [page, setPage] = useState(currentPage),
    [refresh, setRefresh] = useState(0),
    [mobileOpen, setMobileOpen] = useState(false),
    [logoutBusy, setLogoutBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const overview = useResource<Overview>(
    "/overview",
    refresh,
    !!principal,
    page === "overview" && principal ? 15000 : undefined,
  );
  useEffect(() => {
    const controller = new AbortController();
    api<{ principal: Principal }>("/session", {
      signal: controller.signal,
      notifyUnauthorized: false,
    })
      .then((data) => {
        if (!controller.signal.aborted) setPrincipal(data.principal);
      })
      .catch((error) => {
        if (
          !controller.signal.aborted &&
          (!(error instanceof APIError) || ![401, 403].includes(error.status))
        )
          setSessionError(
            error instanceof Error ? error.message : "无法验证会话",
          );
      })
      .finally(() => {
        if (!controller.signal.aborted) setChecking(false);
      });
    return () => controller.abort();
  }, []);
  useEffect(() => {
    const change = () => {
      setPage(currentPage());
      setMobileOpen(false);
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  useEffect(() => {
    const expired = () => {
      if (principal) {
        setPrincipal(null);
        setSessionError("会话已过期，请重新登录。");
      }
    };
    window.addEventListener("soseek:unauthorized", expired);
    return () => window.removeEventListener("soseek:unauthorized", expired);
  }, [principal]);
  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(null), 4500);
    return () => window.clearTimeout(timer);
  }, [notice]);
  useEffect(() => {
    document.title = `${navigation.find((item) => item.id === page)?.label || "管理中心"} · soseek`;
  }, [page]);
  const notify = useCallback((message: string) => setNotice(message), []);
  if (checking)
    return (
      <div
        role="status"
        aria-label="正在验证管理会话"
        className="flex min-h-dvh items-center justify-center bg-[#fafafa]"
      >
        <div className="flex flex-col items-center gap-5">
          <Brand large />
          <Loader2 className="size-5 animate-spin text-zinc-400" />
        </div>
      </div>
    );
  if (!principal)
    return (
      <Login
        key={sessionError ?? "login"}
        sessionError={sessionError}
        onLogin={(value) => {
          setSessionError(null);
          setPrincipal(value);
        }}
      />
    );
  const active = navigation.find((item) => item.id === page) ?? navigation[0];
  const providers = [
    ...new Set(
      (overview.data?.channels ?? []).map((x) => x.provider).filter(Boolean),
    ),
  ];
  const sidebar = (
    <div className="flex h-full flex-col">
      <a
        href="#overview"
        className="block px-6 py-7"
        aria-label="soseek 服务概览"
      >
        <Brand />
      </a>
      <div className="px-6 pt-2 pb-3 text-[10px] font-medium tracking-[0.12em] text-zinc-400">
        工作空间
      </div>
      <nav aria-label="管理导航" className="space-y-1 px-3">
        {navigation.map(({ id, label, icon: Icon }) => (
          <a
            key={id}
            href={`#${id}`}
            aria-current={id === page ? "page" : undefined}
            onClick={() => setMobileOpen(false)}
            className={`group flex h-10 items-center gap-3 rounded-lg px-3 text-[13px] transition-colors focus-visible:outline-2 focus-visible:outline-blue-500 ${id === page ? "bg-zinc-100 font-medium text-zinc-900" : "text-zinc-500 hover:bg-zinc-50 hover:text-zinc-900"}`}
          >
            <Icon
              className={`size-[17px] ${id === page ? "text-zinc-800" : "text-zinc-400 group-hover:text-zinc-600"}`}
              strokeWidth={1.75}
            />
            {label}
            {id === page && (
              <span className="ml-auto size-1.5 rounded-full bg-blue-500" />
            )}
          </a>
        ))}
      </nav>
      <div className="mt-auto px-5 pb-6">
        <div className="rounded-lg border border-zinc-200/80 bg-zinc-50/60 px-3 py-3">
          <div className="flex items-center gap-2 text-xs font-medium text-zinc-600">
            <ShieldCheck className="size-3.5 text-zinc-400" />
            管理员工作空间
          </div>
          <p className="mt-1.5 text-[11px] leading-5 text-zinc-400">
            权限和密钥变更会写入操作审计。
          </p>
        </div>
        <div className="mt-4 flex items-center justify-between px-1 text-[10px] text-zinc-400">
          <span>soseek 管理中心</span>
          <span className="font-mono">
            {overview.data?.version ? `v${overview.data.version}` : ""}
          </span>
        </div>
      </div>
    </div>
  );
  return (
    <div className="min-h-dvh bg-[#fafafa]">
      <a
        href="#main-content"
        onClick={(event) => {
          event.preventDefault();
          document.getElementById("main-content")?.focus();
        }}
        className="sr-only z-50 rounded-md bg-white p-3 text-sm focus:not-sr-only focus:absolute focus:top-2 focus:left-2"
      >
        跳到主要内容
      </a>
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 border-r bg-white lg:block">
        {sidebar}
      </aside>
      <div className="lg:pl-60">
        <header className="sticky top-0 z-20 flex h-[72px] items-center justify-between gap-4 border-b border-zinc-200/80 bg-white/95 px-4 backdrop-blur-sm sm:px-8">
          <div className="flex min-w-0 items-center gap-3">
            <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
              <SheetTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="lg:hidden"
                  aria-label="打开导航"
                >
                  <Menu className="size-5" />
                </Button>
              </SheetTrigger>
              <SheetContent side="left" className="w-64 p-0">
                <SheetTitle className="sr-only">管理导航</SheetTitle>
                <SheetDescription className="sr-only">
                  切换概览、任务、令牌、密钥和审计页面。
                </SheetDescription>
                {sidebar}
              </SheetContent>
            </Sheet>
            <div className="flex items-center gap-2.5 text-[13px]">
              <span className="hidden text-zinc-400 sm:inline">管理中心</span>
              <span className="hidden text-zinc-300 sm:inline">/</span>
              <span className="font-medium">{active.label}</span>
            </div>
          </div>
          <div className="flex items-center gap-2 sm:gap-4">
            <Button
              variant="ghost"
              size="sm"
              className="text-zinc-500"
              onClick={() => setRefresh((value) => value + 1)}
              aria-label="刷新当前页面"
            >
              <RefreshCw
                className={`size-3.5 ${overview.loading ? "animate-spin motion-reduce:animate-none" : ""}`}
              />
              <span className="hidden sm:inline">刷新</span>
            </Button>
            <div className="h-5 w-px bg-zinc-200" />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  className="gap-2 px-1.5 sm:px-2"
                  aria-label={`当前管理员 ${principal.name}，账户菜单`}
                >
                  <span className="flex size-7 items-center justify-center rounded-full border bg-zinc-50 text-xs text-zinc-600">
                    {principal.name?.slice(0, 1).toUpperCase() || "A"}
                  </span>
                  <span className="hidden max-w-32 truncate text-xs font-medium sm:inline">
                    {principal.name}
                  </span>
                  <ChevronDown className="size-3 text-zinc-400" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-52">
                <DropdownMenuLabel>
                  <div className="truncate text-xs">{principal.name}</div>
                  <p className="mt-1 text-[11px] font-normal text-zinc-400">
                    管理员会话
                  </p>
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={logoutBusy}
                  onSelect={async () => {
                    setLogoutBusy(true);
                    try {
                      await api("/session", { method: "DELETE" });
                      setPrincipal(null);
                      setSessionError(null);
                    } catch (e) {
                      notify(
                        e instanceof Error ? e.message : "退出失败，请重试",
                      );
                    } finally {
                      setLogoutBusy(false);
                    }
                  }}
                >
                  <LogOut className="size-4" />
                  退出登录
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <main
          id="main-content"
          tabIndex={-1}
          className="mx-auto max-w-[1480px] px-4 pt-7 pb-12 outline-none sm:px-8 sm:pt-8"
        >
          <h1
            className={
              page === "overview"
                ? "text-[24px] leading-8 font-semibold tracking-tight"
                : "sr-only"
            }
          >
            {page === "overview" ? "服务概览" : active.label}
          </h1>
          {page === "overview" && (
            <p className="mt-1.5 mb-7 text-sm leading-6 text-zinc-500">
              从任务到用量，掌握搜索服务的运行状态。
            </p>
          )}
          {page === "overview" && (
            <OverviewPage
              data={overview.data}
              error={overview.error}
              loading={overview.loading}
              refresh={overview.reload}
            />
          )}
          {page === "runs" && <RunsPage refresh={refresh} notify={notify} />}
          {page === "principals" && (
            <PrincipalsPage
              refresh={refresh}
              current={principal}
              providers={providers}
              notify={notify}
            />
          )}
          {page === "credentials" && (
            <CredentialsPage refresh={refresh} notify={notify} />
          )}
          {page === "audit" && <AuditPage refresh={refresh} />}
        </main>
        <footer className="mx-auto flex max-w-[1480px] items-center justify-between px-4 pb-6 text-[10px] text-zinc-400 sm:px-8">
          <span>soseek · 搜索与研究服务</span>
          <span className="flex items-center gap-1.5">
            <span className="size-1 rounded-full bg-zinc-300" />
            独立身份 · 集中管理
          </span>
        </footer>
      </div>
      {notice && (
        <div
          role="status"
          className="fixed right-4 bottom-5 left-4 z-50 flex items-center gap-3 rounded-xl border bg-white px-4 py-3.5 text-sm shadow-lg sm:left-auto sm:max-w-md"
        >
          <CheckCircle2 className="size-4 shrink-0 text-emerald-600" />
          <span>{notice}</span>
          <button
            type="button"
            aria-label="关闭提示"
            className="ml-auto rounded p-1 text-zinc-400 hover:bg-zinc-100"
            onClick={() => setNotice(null)}
          >
            <X className="size-3.5" />
          </button>
        </div>
      )}
    </div>
  );
}
