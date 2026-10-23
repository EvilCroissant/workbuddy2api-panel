# 更新记录

## 2026-10-23

### 上游同步与会话关联

- 在 develop 集成上游 main，解决 `internal/upstream/headers.go` 重复声明会话 ID 的合并冲突，保留本分支稳定会话映射和必发会话头。
- 保留上游对 `11-128` 通道校验错误与 `11140` 账号限制的分类，避免将通道错误误作账号封禁。
- 根据国内 App `5.8.3.41334564`、国际 App 下载样本 `5.8.4.41366630`（均内置 CLI `2.161.0`）及独立 CLI `2.173.0` 的源码核对，区分会话、轮次、模型调用三层生命周期。国际 App 两次下载哈希一致，但与更新接口声明的 SHA256 不符，未视为完整性校验通过。
- `X-Conversation-ID` 保留稳定的带横线 UUIDv4 格式映射；`X-Conversation-Request-ID` 和 `X-Conversation-Message-ID` 改用真正生成的去横线 UUIDv7，包含生成时的毫秒时间字段。
- 轮次映射最多保留 16384 条，空闲 30 分钟过期；淘汰、过期或进程重启后重新分配。缓存只存键摘要；已有会话和轮次识别仍为启发式，未增加第三方客户端显式会话保证。
- 合法入站 UUIDv7 规范为小写保留；其他轮次标识按会话隔离后映射。工具续调保留轮次 ID，每次独立模型调用生成新消息 ID，HTTP 重放复用该次调用已捕获的 ID。
- `X-Request-ID` 继续等于消息 ID。B3 Span ID 改为独立随机 64 位值，避免使用 UUIDv7 的时间前缀。
- 更新会话格式、并发分配、缓存淘汰、工具续调、新轮次及 HTTP 重放的回归测试；模型目录测试改用假上游，避免依赖外部服务。
- Docker 内验证通过：`go test ./...`、启用 Node 的 `go test -json ./internal/panel`、`go test -race ./internal/session ./internal/upstream ./internal/server`、`go vet ./...`、注入版本的服务端编译及 `go build ./cmd/...`。

### 验证边界

- 官方客户端行为来自静态源码追踪；未使用真实账号验证服务端校验，也未将 `14003` 归因于会话头差异。

## 2026-10-16

### 上游请求身份修正

- 国内版默认 UA 修正为 `WorkBuddy/5.8.2 WorkBuddy/5.8.2 CLI/2.161.0`。
- 国际版默认 UA 修正为 `WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0`。
- `X-IDE-Version` 按 realm 对齐官方 App 版本：国内 `5.8.2`，国际 `5.8.4`。
- 主 chat 请求固定发送 `X-Product: SaaS`，与 `client_name` 解耦。
- `client_name` 仅控制 `X-IDE-Name/Type`；配置为 `SaaS` 时保留旧行为，不注入 `X-IDE-*`。
- 国内桌面任务 UA 与桌面事件指纹同步更新为 App `5.8.2`、CLI `2.161.0`。

### 会话渠道头修正

- `X-Conversation-ID`：按会话键稳定派生 UUID v4。
- `X-Conversation-Request-ID`：按会话和轮次稳定生成 32 位 hex。
- `X-Conversation-Message-ID`：每次出站新建 32 位 hex。
- `X-Request-ID` 与 `X-Conversation-Message-ID` 相同。
- `X-Root-Request-ID` 与 `X-Conversation-Request-ID` 相同。
- 修复 issue #188 对应的 `conversation id missing` 问题。

### 验证

- `go test ./...`
