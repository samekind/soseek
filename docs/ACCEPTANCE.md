# soseek 0.1.0 验收记录

日期：2026-10-17，Asia/Taipei。真实请求经过本机 CLI → 公网 HTTPS → 云端 soseekd → 配置的上游。原始响应、事件和私有运维证据存放在仓库外；本文不包含令牌或私人 endpoint。

## 已通过的真实路径

| 范围 | 观察与结论 |
|---|---|
| 公网入口 | 有效 TLS；实际 CLI 登录、doctor、动态能力发现成功 |
| 日常搜索 | Exa、Tavily、Serper、Firecrawl 均有真实成功记录；Serper 也曾返回 429 |
| 同题比较 | 24 道公开题 × Exa / Tavily / 两者融合，72 次任务全部成功，见 [评测报告](../evals/RESULTS.md) |
| 网页阅读 | Firecrawl、Tavily、Exa 分别读取公开网页并返回正文 |
| 原生扩展 | Exa find_similar、Firecrawl map 和单页 crawl 成功；Tavily map 接受请求但本例为空结果 |
| 固定流程 | 显式 pipeline 实际组合 Exa/Tavily 搜索并读取网页，任务成功 |
| Grok 单题研究 | 真实 search → read → inspect → finish → 有引用答案，任务 succeeded / goal_completed；核对 SQLite 原文确实支持“同时只有一个 writer” |
| Grok 多来源研究 | 模型选择 Exa/Tavily/Serper，读取三份官方页面并检查正文，交付引用答案；状态为 partial / evidence_incomplete，明确未覆盖的扩展问题，详见下文 |
| 两身份隔离 | 两个人的原令牌均能访问本人结果；对方 run/result/events/artifact 均返回 404；无效 token 为 401 |
| 幂等与取消 | 同 key 同正文获得同 run；改正文为 409；显式取消到达 cancelled |
| SSE 与 artifact | 公网事件流可读取，Last-Event-ID 后仅收到后续事件；本人 artifact 可下载 |
| 备份恢复 | 实际云端快照下载、完整性检查、原身份认证、结果逐份 hash 一致，以及不确定请求不重放，见 [备份报告](BACKUP-ACCEPTANCE.md) |

这些是指定时间与请求的验收，不是长期可用性保证，也不是所有原生参数的穷举测试。动态 discovery 区分已实现的操作和每条线路的实测状态。

## Grok 原生搜索的实际限制

Responses 原生搜索返回了综合文本、引用、source ID 关联、检索事件及上游报告费用。一条真实请求要求 `max_tool_calls=2`，上游实际报告 3 次；另一任务也出现内部调用超限。因此服务返回 `partial / upstream_limit_exceeded` 并停止后续调用，保留原始证据和实际费用。

这条第三方线路的内部工具次数只能请求和事后核对，不能承诺硬限制。Grok planner 的外部工具调用仍由 soseek 控制；原生搜索是可选工具，不要求每次研究调用。不能用 HTTP 200 或可用的模型列表代替这个边界。

## 有意保留的故障证据

开发中的真实长文任务曾因只命中目录、短语跨行、重复全局证据导致 context limit、以及已读页被展示筛选遮蔽而不完整。修复采用本地 inspect、多命中、原文位置、逐次工具事件、按 URL 唯一正文增量计量和有界上下文管理；没有增加 planner 轮数、预算或正文上限。

最终多来源复测完成 Exa/Tavily/Serper 检索、三次官方页面阅读、inspect 和 finish。被遗漏的 PostgreSQL explicit-locking 页现在立即返回可见来源 ID，17,312 字符的正文片段保留到最终结果；总交付正文 59,895 字符，仍在原字节上限内。核对来源支持 SQLite 单 writer 与 PostgreSQL 同行锁冲突的主要结论，没有再发生 context limit 或已读来源丢失。

模型主动保留两项扩展缺口：未核对 SQLite BEGIN CONCURRENT 的正式发布状态、未展开 PostgreSQL 各隔离级别的错误码和重试语义。因此最终状态是 `partial / evidence_incomplete`，不是 `succeeded`。这条记录证明完整多来源工具路径与证据交付可用，不把未回答的问题算作完成。

另一次验收在首个 planner 请求遇到上游 HTTP 500，服务返回 `failed / planner_failed`、保留原调用的 uncertain 和费用预留，没有自动重发。随后独立的新验收任务完成了上述链路；这不能证明上游从此稳定。

未知美元费用保留预算：例如基线评测报告了 $0.336 的 Exa 费用和 48 Tavily credits，Tavily 美元费用未明确，另保留 $2.40。预留不是实际账单，不能把 reported=0 解读为免费。

## 构建与自部署

- `go test -race ./...` 与 `go vet ./...` 通过。
- 评测停止逻辑的 4 项 Node 测试通过，覆盖 429/Retry-After 保存记录后停止且不重试。
- macOS、Linux、Windows × arm64/amd64 CLI 可交叉构建；Linux 包含 server。发布档案有 SHA-256 校验和。
- Docker 自部署在 Linux ARM64 完成过 9 项完整检查；最终源码又在 Linux AMD64 完成构建与 5 项隔离运行检查，并导出镜像归档、校验 SHA-256 和 load roundtrip。均使用本地 HTTP fixture，不能替代上述真实上游验收；详见 [部署指南](../deploy/README.md)。
- Skill 经受管流程同步并检查；上游密钥仅在服务端，CLI 使用个人令牌。

## 当前边界

首版采用单进程与本地 SQLite WAL，没有多机调度、HA 或大规模容量测试。快照已验证能在另一台机器恢复，但周期备份仍在同一部署主机，不等于自动异地灾备。

Firecrawl crawl 已持久化上游 job ID、分页与费用快照，但没有用户可调用的跨 run 接管/恢复 API；中断后不自动重发可能已计费的作业。代抓服务的逐跳 DNS、重定向和新发现链接不能由 soseek 完全控制。

Grok 引用 ID 有机械检查，语义仍需要按原文核验；本次人工检查的是列出的样例，不保证任意回答。其他 Grok 模型、X Search、图像生成、所有硬过滤参数均未验收。没有公开注册、收费或可视化后台。

源码按 MIT License 提供。发布包由 `scripts/release.sh` 构建；以项目实际 Release 页面列出的资产为准。GitHub CI 使用本地 fixture，不读取或调用部署者的真实上游账户。
