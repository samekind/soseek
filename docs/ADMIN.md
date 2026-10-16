# 管理面板

管理面板入口为服务的 `/admin/`，使用 React、TypeScript、Vite 和 shadcn/ui，静态资源随 `soseekd` 一起发布。浏览器通过同源 `/admin/api` 管理服务；部署完成后不需要 Node.js 进程、独立前端服务器或额外前端域名。

这里记录管理功能的使用和部署约定。实际构建、浏览器和线上验收结果以 [验收记录](ACCEPTANCE.md) 为准；旧版 CLI/容器验收不代表新增面板已经通过验收。

## 首次登录

管理权限需要在服务器本地授予。已有访问令牌继续使用，但普通调用者不会因升级自动成为管理员。

```sh
# 找到已有令牌对应的 principal ID；列表不回显令牌明文。
soseekd admin token-list --config /etc/soseek/config.json

# 对选定的有效 principal 授权。此命令在服务器本地执行。
soseekd admin grant --config /etc/soseek/config.json --id PRINCIPAL_ID
```

使用容器时可在命令前加 `docker compose exec soseek`；使用 systemd 时以有权读取私有数据库的 `soseek` 用户执行。需要新建身份时，先使用 `soseekd admin token-create`，妥善保存其一次性返回的令牌，再对返回的 principal ID 执行 `grant`。

打开 `https://你的服务地址/admin/`，输入已获授权的个人令牌。令牌用于建立管理会话，浏览器后续使用 `HttpOnly`、`SameSite=Strict` 会话 Cookie，HTTPS 下启用 `Secure`，有效期为 12 小时。面板不把令牌存进 localStorage、sessionStorage 或 URL。退出面板注销本次会话；它与注销 CLI、吊销个人令牌是不同操作。

管理 API 要求同源访问。写操作校验准确的 `Origin` 和管理请求头，不提供跨域 CORS。开发时只允许回环地址的 HTTP；远程访问使用 HTTPS。不要通过另一个域名承载静态页面再跨域调用管理 API。

## 能管理什么

| 页面/对象 | 管理能力 | 边界 |
|---|---|---|
| 概览 | 查看版本、配置 revision、任务状态、已报告/估算/预留用量 | 这些数值不等于上游账号的实时剩余额度 |
| 个人令牌 | 创建调用者、修改名称/每日预算/允许的 provider、轮换或吊销令牌 | 新建和轮换时明文只显示一次；网页不能授予管理员权限 |
| 上游凭据 | 替换既有凭据、停用、恢复部署配置中的原凭据 | 不能添加 provider/channel、改变 endpoint、模型或能力声明 |
| 任务 | 查看全部用户的任务、结果、调用明细和失败信息，取消任务 | 取消不能撤回已经发出的上游请求，也不保证退款 |
| 审计 | 查看操作人、动作、对象与时间 | 审计不保存个人令牌或上游 key 明文 |

**管理员可以查看全部用户任务与正文。** 管理权限应只授予需要承担服务管理职责的人。普通 caller 的 `/v1` 请求仍按 principal 隔离，知道任务 ID 不能读取他人的记录；管理员会话带来的全局可见性也不会改变普通 Bearer API 的归属检查。

轮换保留身份及其历史，只替换访问令牌；旧令牌和该身份已有管理会话失效。吊销会阻止新请求并请求取消尚未完成的任务。管理面板不能轮换或吊销当前登录管理员自身，也不能吊销最后一名有效管理员；管理员身份的引导与恢复仍由服务器本地命令完成。

## 上游凭据覆盖与持久化

面板按部署配置中的既有凭据引用管理 key。同一个文件引用或环境变量引用被多条 channel 共用时，一次替换、停用或恢复会作用于所有关联 channel；界面列出这些关联关系。

- **替换**：写入新的 key 并启用该凭据引用。列表只返回是否配置、是否覆盖、是否停用等元数据，不回读 key，也不展示 key 后缀。
- **停用**：保存停用标记，不能悄悄退回原部署 key 继续调用。
- **恢复部署配置**：删除运行时覆盖，重新读取原 `key_file` 或 `key_env`。这不会修改 provider/channel 定义，也不会启用部署配置本身已停用的 channel。

覆盖文件保存在 `dirname(database)/credentials`，默认数据库为 `/var/lib/soseek/soseek.db` 时，对应 `/var/lib/soseek/credentials`。目录权限为 `0700`，文件为 `0600`，归服务用户所有；服务拒绝不安全的权限或软链接。原 `/etc/soseek/credentials` 可以继续只读挂载，面板不修改它。

升级和回滚二进制时必须保留整个状态目录中的 `credentials` 子目录。Compose 的持久化 `data` 卷与 systemd 的 `/var/lib/soseek` 都需要保留它。删除或重建此目录会丢失覆盖 key 和停用状态；恢复时还应保留原配置中的凭据引用路径/环境变量名称，因为覆盖对象按引用身份关联。

部署中移除或改绑凭据引用后，原覆盖文件会保留以支持回滚，但不再作用于新的引用。它仍属于私有 secret，应按同一备份和保留策略处理；确认不再需要回滚后再由运维人员清理，不自动删除。

`soseekd backup` 只生成 SQLite 快照，**不包含这个凭据目录**。完整恢复需要：数据库快照、部署配置及原始 secret、私有凭据覆盖目录。将覆盖目录纳入独立的受保护 secret 备份；不要把它与可公开分发的镜像、源码、普通日志或数据库附件一起上传。数据库恢复后还应核对快照之后的令牌吊销、管理员授权和配额变更。

## HTTPS 反向代理

面板与 API 使用同一个外部 origin。原生 systemd 部署中的回环反代可以沿用既有配置。容器部署时，服务看到的来源地址可能是 Docker 网关或反代容器的地址；因此不能假定宿主 `127.0.0.1` 端口映射会让容器也看到回环来源。

需要转发 HTTPS 状态时，在服务配置的 `trusted_proxies` 中显式填入实际反代来源的精确 CIDR：IPv4 使用 `/32`，IPv6 使用 `/128`。默认仅信任回环来源，不信任任意客户端传入的 `X-Forwarded-Proto`。不要配置 `0.0.0.0/0`、`::/0` 或随意扩大到整个私网。

可先读取所用网络的网关或反代容器 IP，再按真实拓扑配置，而不是复制固定子网：

```sh
docker network inspect NETWORK_NAME --format '{{range .IPAM.Config}}{{println .Gateway}}{{end}}'
docker inspect CADDY_CONTAINER --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}'
```

宿主进程反代通常经过容器网关；同一 Docker 网络内的反代容器则可能使用其自身地址。以服务实际看到的来源为准，只信任该跳点，并在变更地址后同步配置。服务端口继续限制为回环或受保护的内部网络。

## 构建与开发

前端构建需要 Node.js 24 或更新版本，以及 `web/package.json` 中固定版本的 pnpm。CI 和 Docker 使用 Node.js 24 与 Corepack；运行中的 `soseekd` 不需要 Node/pnpm。

```sh
# 仓库根目录：冻结安装、TypeScript/Vite 构建并同步 Go embed 文件。
./scripts/build-web.sh

# 再编译可直接服务 /admin/ 的服务器。
go build -o bin/soseekd ./cmd/soseekd
```

脚本在 `web/` 中执行 `pnpm install --frozen-lockfile` 和 `pnpm build`，检查入口包含 `/admin/assets/` 下真实存在的 JavaScript，再把 `web/dist` 复制到 `internal/adminui/dist`。生成文件不入 Git；只保留受版本管理的 `placeholder.txt`。普通 `go test`/`go build` 可以在未装 Node 的 checkout 中运行，但未嵌入真实前端的服务会在 `/admin/` 返回 503 和构建提示，不能作为含管理面板的发布物交付。

`scripts/release.sh` 在跨平台编译之前自动执行该脚本。Docker 先在 Node 构建阶段生成前端，再把产物复制到 Go 构建阶段；最终运行镜像只有 Go 二进制、系统证书和时区数据。CI 先构建并检查前端产物，再执行 Go 测试、vet 与镜像构建。

本地调试可使用 `pnpm --dir web dev` 查看样式，但浏览器会话/API 必须通过同源集成路径测试。正式验收使用已嵌入资源的 Go 服务，覆盖登录退出、权限拒绝、令牌操作、上游凭据覆盖、任务访问、重启持久化与备份恢复；仅 Vite 编译成功不能证明这些流程已通过。

## 验证记录（2026-10-17）

已在本地隔离服务中通过浏览器操作验证：管理员登录、令牌创建/额度修改/轮换/吊销、一次性令牌展示、上游密钥更新/停用/恢复、任务正文与来源查看、审计记录，以及 390px 手机导航和页面宽度。上游使用本地 Exa fixture，没有为面板验收发起付费搜索。

已有 HTTPS 部署完成程序升级，原配置和原凭据逐项比对一致；管理会话 Cookie 实测为 `HttpOnly`、`Secure`、`SameSite=Strict`。五个管理读取接口正常返回，浏览器本地存储没有访问令牌，缺少管理请求头的写操作返回 403。已确认匿名和 caller 不能访问管理数据，原 CLI 认证诊断仍成功。真实上游密钥没有为了验收被轮换或停用。

代码验证包括完整 Go race 测试、`go vet`、TypeScript/Vite 构建及五项前端安全行为测试。管理 API 测试覆盖权限隔离、同源检查、代理信任、令牌失效和密钥操作；凭据与存储测试覆盖原子写入、持久化、审计事务与路径限制。旧容器验收记录不代表新面板的容器运行验收；新 Docker 构建由 CI 单独验证。
