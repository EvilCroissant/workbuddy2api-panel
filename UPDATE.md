# 更新记录

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
