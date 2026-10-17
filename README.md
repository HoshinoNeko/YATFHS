# YATFHS

Yet Another Temporary File Hosting Service，Go 编写的临时文件托管服务。前端和管理后台内嵌在二进制中，无数据库及前端构建依赖。

## 启动

需要 Go 1.23.4 或更新版本：

```sh
go run . -config config.json
```

默认监听 `:8080`，访问 `http://localhost:8080`。文件、元数据、动态设置和日志保存在 `storage_dir`（默认 `./data`），升级时请保留整个数据目录。旧版的文件目录与下载链接可继续使用。

Docker Compose 会构建当前仓库源码：

```sh
docker compose up -d --build
```

## 管理后台

1. 用 `openssl rand -hex 32` 等方式生成并保存一个随机令牌，至少 32 字符。
2. 将 `YATFHS_ADMIN_TOKEN` 环境变量设置为该令牌，再启动服务；Compose 可通过同目录 `.env` 设置此变量。
3. 打开 `/admin`，输入令牌登录。

未配置令牌时，所有管理接口关闭。令牌也可通过配置文件的 `admin_token` 设置，环境变量优先。网页登录只在当前标签页内存中保留令牌；刷新后需要重新登录。远程使用时请启用 HTTPS。

后台提供：

- 启动时间、运行时长、内存、goroutine、活跃文件数量/大小、请求/错误次数、上传/下载数量及字节数。
- 文件列表、上传 IP、过期时间和批量 ZIP 下载。
- 最近 500 条请求、上传、下载、清理、管理操作与启动日志。磁盘日志为 `events.jsonl`，达到 5 MiB 后轮转，保留一个备份；统计计数自本次进程启动开始，日志可跨重启查看。
- 公共上传的最短、默认、最长有效期，以及网页展示的有效期按钮（秒）。前台与 API 共用这组限制，服务端强制校验。
- 管理员上传可使用 `expire=0` 永久保存，也可选择自定义秒数或具体时间，最长 10 年。永久文件不会被到期清理任务删除。

动态策略原子保存到数据目录的 `settings.json`，立即生效并跨重启保留，优先于 `config.json` 的初始有效期设置。不需要让容器中的配置文件可写。策略修改不追溯修改已完成的文件；未完成的公共上传在合并时重新校验。

## 网页上传和批量下载

支持拖拽或选择多个文件；浏览器使用 4 MiB 分片，显示各文件百分比与字节进度，可暂停、重试，并自动跳过服务端已有分片。网络错误最多自动尝试 3 次。

刷新页面后，在 **24 小时内重新选择同一份文件** 并使用相同的过期选项即可续传；服务重启后同样可恢复。浏览器保存上传会话引用，并通过覆盖完整文件内容的 SHA-256 指纹防止混用文件。普通 HTTP 内网环境也有 SHA-256 与复制功能的兼容实现。文件过期时间从上传合并完成时开始计算。

点击 Remove 可取消未完成的上传并删除分片。未完成的分片在 24 小时后由清理任务回收。会话 ID 是随机访问凭证，应与下载链接一样保密；管理员会话每一步还需要管理员令牌。

可勾选上传结果或后台文件，也可在首页粘贴同站点下载链接，生成最多 100 个文件的 ZIP。ZIP 由服务端流式输出、浏览器直接下载，同名文件自动重命名。

## HTTP API

页面内的 API Reference 会自动使用当前站点的完整地址。

```sh
# 普通上传；省略 expire 使用当前默认值
curl -F 'file=@./example.zip' -F 'expire=3600' 'http://localhost:8080/api/v1/upload'

# 管理员永久上传；先在当前 shell 设置 YATFHS_ADMIN_TOKEN
curl -H "Authorization: Bearer $YATFHS_ADMIN_TOKEN" \
  -F 'file=@./example.zip' -F 'expire=0' 'http://localhost:8080/api/v1/upload'

# 当前统计、文件大小限制及有效期策略
curl 'http://localhost:8080/api/v1/stats'
```

成功上传响应（永久文件的 `expires_at` 为 `null`）：

```json
{
  "status": "success",
  "data": {
    "url": "http://localhost:8080/{id}/example.zip",
    "id": "{id}",
    "name": "example.zip",
    "size": 123,
    "expires_at": "2026-10-18T12:00:00Z"
  }
}
```

| 接口 | 用途 |
| --- | --- |
| `POST /api/v1/upload` | 兼容旧版 multipart 单文件上传，字段 `file`、`expire` |
| `GET /api/v1/stats` | 公共统计、`max_file_size`、`limits`、分片大小及会话时限 |
| `POST /api/v1/uploads` | 创建分片会话，JSON：`name`、`size`、可选 `expire` 和 `fingerprint` |
| `GET /api/v1/uploads/{session}` | 查询 `uploaded_chunks`（从 0 开始）、`chunk_size`、`expires_at`；已合并时返回 `result` |
| `PUT /api/v1/uploads/{session}/chunks/{index}` | 原始二进制分片；每片恰好 4 MiB，最后一片使用剩余大小，可重复提交 |
| `POST /api/v1/uploads/{session}/complete` | 校验完整性并合并；重复调用返回同一个结果 |
| `DELETE /api/v1/uploads/{session}` | 取消会话、删除临时分片；不会删除已经完成的文件 |
| `GET/HEAD /{id}/{filename}` | 下载文件，支持 Range 断点下载 |
| `POST /api/v1/downloads` | JSON：`{"ids":["id1","id2"]}`，返回 ZIP；也支持表单 `ids` 字段包含 JSON 数组 |
| `GET /api/v1/admin/overview` | 受保护的运行统计、策略、最近日志 |
| `GET /api/v1/admin/files` | 受保护的活跃文件列表与下载链接 |
| `GET/PUT /api/v1/admin/settings` | 查询/保存公共有效期策略 |
| `POST /api/v1/admin/cleanup` | 立即清理过期文件及会话 |

管理接口及管理员上传使用 `Authorization: Bearer <token>`。有效期策略的 PUT 请求体为：

```json
{
  "min_expire_sec": 60,
  "default_expire_sec": 3600,
  "max_expire_sec": 172800,
  "expire_options_sec": [3600, 14400, 43200, 86400, 172800]
}
```

要求 `1 <= min <= default <= max <= 315360000`，展示按钮为范围内的 1–20 个不重复整数。分片会话在创建时按整个文件大小预扣本小时上传配额；重试分片不重复扣费，取消不退还配额。每个 IP 最多 10 个未完成会话，全局最多保留 1000 个会话，上传速率限制按文件创建计数。

## 配置与反向代理

`config.json` 可设置监听地址、存储位置、单文件上限、初始有效期、清理周期、每 IP 请求速率/小时上传配额和 IP 白名单。

- `base_url` 默认为空，下载链接采用请求的协议与 Host；也可显式设置公开站点地址。文件名会进行 URL 编码。
- `trusted_proxies` 默认为空。只有来自所配置 IP/CIDR 的请求才会采用 `X-Forwarded-For` / `X-Real-IP` 和 `X-Forwarded-Proto`；例如反代在本机时配置 `["127.0.0.1/32", "::1/128"]`。反代应正确覆盖或追加代理头，且转发原始 Host。
- 如果反向代理来自白名单地址，请正确配置 `trusted_proxies`，以便按真实客户端 IP 限速。不要信任不受控制的代理网段。
- 反向代理需允许 PUT/DELETE；分片请求体上限至少 4 MiB，旧 multipart 接口则需支持单文件上限加约 1 MiB 的表单开销。ZIP 下载应配置足够的超时。
- 配置文件缺省值和旧版文件布局仍兼容。过期文件在服务启动时及周期清理时删除。

当前存储采用单进程文件锁，部署时应使用单实例；多副本共享同一数据目录需要额外的协调存储。

## 验证

```sh
go vet ./...
go test -race ./...

# 仅浏览器回归测试需要 Node.js 与 Playwright；应用运行本身不需要 Node.js
npm ci
npx playwright install chromium
npm run test:browser
```

macOS 上浏览器测试自动使用已安装的 Chrome，也可用 `CHROME_PATH` 指定可执行文件。测试自动启动独立服务并使用临时数据目录，覆盖拖拽多文件、暂停、刷新续传、文件校验、ZIP、后台权限、永久上传及策略修改，截图写入 `test-results/`。
