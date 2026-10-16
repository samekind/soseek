# Grok 接入与验证边界

更新：2026-10-17。本文区分最初协议探测与真实云端验收，未记录私人 endpoint、密钥或账号信息。

## 1. 三个角色

- **Planner**：理解目标、选择工具、处理结果、发现缺口和决定停止。
- **搜索 provider**：调用 Grok 自带 Web Search，返回检索事件、来源、引用与综合文本；可单独用，也能由 planner 选择。
- **图像模型**：imagen 已有的 grok-image 接法提供 endpoint/认证参考；图像生成不是当前搜索服务的新增产品范围。

Grok 原生搜索与调用外部搜索 API 都是首版目标内的路径，不能把它限定成只在最后写答案的模型。

## 2. 本机接法已核对

Grok CLI 的受管与运行配置均将默认别名指向 `grok-4.7`，使用 `chat_completions`，另配置 `grok-4.6`、`grok-4.7-build-fast`。这些是读取到的配置，本次推理只验证 `grok-4.7`。

imagen 的 `grok-image` 别名指向 `grok-imagine-image`，Grok 适配器走 `/images/generations`。其本机配置的一条线路与 Grok CLI 具有相同 base URL 和相同 key 内容（只比较相等性，不输出值）。这说明可以复用现有线路和认证，不意味着 image model 可以直接承担聊天/搜索。

Grok CLI 文档说明 `web_search` 有服务端搜索和客户端兜底两条路径，并支持单独配置搜索模型。因此 CLI 里出现 web_search 或 `/models` 返回 Grok，不足以证明特定 API 线路的搜索行为；本次另外执行了实际 API 验证。

## 3. 2026-10-16 最初协议探测

| 验证 | 请求与观察 | 证明到哪一层 |
|---|---|---|
| 模型发现 | 现有线路 `/models` HTTP 200，列出聊天与图像模型 | 认证和模型发现 |
| 原生 Web Search | `/responses`，模型 `grok-4.7`，`tools=[{type:web_search}]`，required tool choice，max_tool_calls=2 | HTTP 200；两个 completed web_search_call，含 query 和来源 URL，最终文本含 url_citation |
| 外部工具选择 | `/chat/completions`，注册 `search_web(query)`，指定 tool choice | HTTP 200；返回结构化 function call 与合法 JSON 参数 |
| 外部工具闭环 | 用上述 query 真正调用 Exa，回传 tool_call_id 和来源；再次请求同一 Grok 模型 | Exa 与 Grok 均 HTTP 200；Grok 根据返回来源选出 Go 官方文档并结束 |

请求模型为 `grok-4.7`，本次响应模型字段为 `grok-4.7-build`。两者都保存；不能修改记录掩盖线路返回的模型标识差异，也不能仅凭标识推断底层模型内部实现。

这次早期外部工具闭环使用 forced tool choice，仅证明协议可用。随后实现并部署了 soseek 服务，另做真实 CLI 验收，见 [ACCEPTANCE.md](ACCEPTANCE.md)；不能把早期探测单独当成完整产品验收。

## 4. 初始适配方式

以现有线路为 channel 配置输入，不让服务端依赖本机 grok/imagen 的进程、会话或凭据路径。部署时通过受管 secret 引用提供 API key。

- 编排模型 transport：先使用已验证的 Chat Completions function calling；记录 assistant tool_calls 和对应 tool 消息，完成每一步持久化。
- `grok.search` transport：使用已验证的 Responses + native web_search；独立限制 token、服务端工具调用数和总时间，保存 output 检索事件与 annotations。
- 同一 run 可以选择 Grok 原生搜索、Exa/Tavily/Serper 搜索、Firecrawl 阅读；费用进入同一总预算。
- 首个实现不要求一个 API 请求同时混用原生工具与自定义函数。若后续统一到 Responses，混合工具、多轮上下文及中转透传仍需单独验收，不能从两个独立路径成功推断出来。
- `grok.search` 不暴露 planner 递归入口。一次搜索内部工具调用也计入 run 上限；同一个费用字段若已含工具费，不能再重复加一份工具费。

## 5. 返回内容与融合

标准结果通过 `answer`、`results`、`citations`、`retrieval_events` 和 `usage` 保留原生输出。每条 citation 的 `source_id` 对应本 run 来源；`answer_text` 是该条上游注释所属文本，offset 按 upstream 原值保存（含 0），不重定位到后续综合答案。只有 Grok 引用一个 URL，不等于 soseek 已抓取该网页全文。

来源按统一 URL 规则去重；同一 Grok 搜索内多次检索到同一 URL，不增加 provider 票。缺少可信原始 rank 时省略该字段，不用模型引用顺序伪造 RRF rank。与其他引擎组合时，将其来源作为补充证据保留；被引用来源不会因展示 limit 被剪掉。

保留搜索输出中的域名过滤请求与实际来源，不能只看 HTTP 200 就宣称所有过滤选项严格生效。本次仅验证允许域为 docs.x.ai 的一个例子，不覆盖所有过滤、图像搜索或 X Search 参数。

## 6. 用量与剩余验证

本次 Responses 返回了 input/output tokens、两次服务端 web search 和 `cost_in_usd_ticks`。官方定义 10^10 ticks 等于 1 USD；本次值对应上游报告约 USD 0.01791724。代理自身如何扣款仍须核对。本次两个 Chat Completions 响应未返回该费用字段，应标 unknown/estimated，不能记 0；真实 Exa 查询另报告 USD 0.007。

云端真实原生搜索已返回结构化引用、检索事件及费用；其中一次请求 `max_tool_calls=2`，上游实际报告 3 次调用。执行器据此返回 `partial / upstream_limit_exceeded`，保留实际费用与证据并停止后续动作。因此当前第三方线路不能宣称严格遵守内部工具次数限制，配置预算也不能撤销已发生的上游费用。

两种 transport 的实际 cloud CLI 路径、长文检查、恢复与引用验收见 [验收记录](ACCEPTANCE.md)。长期稳定性、所有过滤选项、X Search、其他模型和单请求混用 native/custom tools 仍未验证。机械检查引用 ID 不等于人工核验每条推断。

## 7. 公开协议依据

- [xAI Web Search](https://docs.x.ai/developers/tools/web-search)
- [xAI Function Calling](https://docs.x.ai/developers/tools/function-calling)
- [xAI Cost Tracking](https://docs.x.ai/developers/cost-tracking)

官方能力文档描述协议；是否可用于本部署以指定中转线路的实测为准。当前证据说明无需先另购一条 Grok 线路才能开始开发，不保证现有线路的长期稳定性或全部原生功能。
