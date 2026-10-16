# soseek 0.1.0 实现记录

更新：2026-10-17。架构基线见 [DESIGN.md](../DESIGN.md)，真实验证与边界见 [ACCEPTANCE.md](ACCEPTANCE.md)。本文描述已实现的首版。

## 已实现

- Go `soseek` 薄客户端与 `soseekd` 服务；HTTPS、个人令牌、动态能力与参数发现、OpenAPI。
- Exa、Tavily、Firecrawl、Serper、Grok 适配器；官方与中转分为不同 channel，账号共享 quota group。
- 单家搜索、显式多家融合、网页阅读、原生操作，以及 Grok 云端编排和显式固定流程。
- 保守 URL 规范化、按 provider 去重投票的 RRF、域名多样性、稳定来源 ID；Grok 无排名引用保留为补充证据。
- SQLite WAL/FULL、个人数据与缓存隔离、有界 worker、公平领取、幂等提交、持久化 run/attempt/events/checkpoint、SSE 续取、取消。
- 请求/调用/页数/正文/模型轮数/时间/费用边界、认证隔离与 SSRF 检查。未知收费保留预留，不把 credits 当成美元。
- 云端 systemd 部署、HTTPS、独立每日备份；通用 Docker Compose 自部署与恢复说明。
- 六平台 CLI 发布包、Linux server、校验和、安装脚本、canonical Skill。构建发布包不会自动上传 GitHub。

## 代码职责

| 路径 | 职责 |
|---|---|
| `cmd/soseek`、`internal/client` | CLI、认证、发现、提交恢复、等待与输出 |
| `cmd/soseekd`、`internal/api` | 服务、管理命令、HTTP/SSE、worker |
| `internal/core`、`internal/config`、`schemas` | 类型、配置检查、公开协议 |
| `internal/store` | 事务、预算、持久化、恢复、备份和留存 |
| `internal/provider` | 上游协议、参数映射、响应与费用解析 |
| `internal/engine`、`internal/fusion` | 路由、有界执行、Grok 工具循环、正文检查与融合 |
| `skills/soseek` | 宿主 Agent 调用指导 |
| `deploy`、`scripts`、`evals` | 自部署、安装发布、真实验收与公开问题集 |

## 初始运行约束

当前采用一个服务进程、2 个 worker、队列 20、每身份同时执行 1 个任务；上游还受共享账号的 RPM/并发约束。默认每次预算上界 $1、允许的最高请求预算 $2，部署每日 $10，两名用户各每日 $5。这些是管理员限制，不是套餐价格或实际账单承诺。

单次最多 12 次上游工具调用、6 轮 planner、6 页、60,000 字符、180 秒。模型输出和上下文另外有界。Grok 原生工具调用在第三方服务内部执行，本服务只能提交限制并核对返回的实际计数；超出时保留证据、标记 partial 并停止后续调用，不能撤回已发生的上游费用。

费用按 UTC 日记账。未结算调用跨日继续占用额度，管理员可用 `soseekd admin settle` 按核对账单结算，或以原始 ceiling 作为明确标注的保守估算。不能通过重启、取消或换 channel 清掉未知费用。

## 故障与恢复语义

CLI 收不到提交确认时保留原请求与幂等键；恢复时重用它们。收到 run ID 后删除待确认记录中的正文。Ctrl-C 只结束本机等待，云端取消必须显式调用。

服务重启保留排队工作和完成结果；已发出而结果未持久化的 attempt 标为 uncertain，停止该任务并保留已有证据，不自动重放可能已计费的请求。不提供端到端 exactly-once 保证。

Firecrawl crawl 在轮询前持久化上游 job ID，处理分页并保留费用快照。首版没有面向用户的跨 run job 接管、恢复与取消 API；中断后不自动创建新 crawl 作为替代。外部代抓服务自行解析的 DNS、重定向和新发现网页不受本进程逐跳控制。

## 明确后置

公开注册、支付、可视化管理、商业租户、跨用户内容缓存、私有 cookie 抓取、本机直连执行、X Search、图像生成、多副本调度与跨区域 HA。场景文档中的持续监测与历史来源增量复用也是后续接口。

SQLite 仅用于单实例本地持久盘；不能由多实例共享文件，也不能放 NFS。容器重启与快照恢复有独立验收，未做多机调度、高可用或生产规模压测。
