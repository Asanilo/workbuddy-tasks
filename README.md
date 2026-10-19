# wb2api-tasks

WorkBuddy / CodeBuddy 成长任务独立 CLI。**不启动 HTTP 代理**，只做：

```
OAuth 登录（可选）→ 读 auths → 查询任务 → 接受/执行/领奖 → 输出结构化结果
```

从 [workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel)（MIT）中抽取任务相关代码，保留原许可与版权声明。

## 为什么是 CLI

- 旧反代继续当模型代理，本工具只负责“扫荡/养号”；
- 不需要跑第二个 7863 端口，也不碰 DSH 流量；
- 适合 cron / systemd timer / AI agent 调用。

## AI 友好约定

给 agent 使用时，请遵守：

1. **总是传 `--json`**。stdout 只输出 JSON，日志全部走 stderr。
2. 先 `run --dry-run --json` 看 `pending`，再 `run --json`。
3. 登录流程用 `login --ndjson`：会先输出 `{"event":"login_url"}`，用户打开 URL 后
   自动输出 `login_ok` / `sweep` / `error`；不要等交互提示。
3. 解析顶层 `ok`、`accounts[].status`、`accounts[].results[].status`。
4. 不会等待 stdin，也没有交互确认；`run` 就是执行。
5. 稳定退出码：

| 退出码 | 含义 |
|---|---|
| 0 | 全部成功 |
| 1 | 部分账号/任务失败（看 `accounts[].error`） |
| 2 | 用法错误 |
| 3 | 没有可用账号/凭据加载失败 |
| 4 | 上游错误（保留） |

6. `schema` 命令输出输出结构说明，可给 agent 做自描述。

## 构建

需要 Go 1.22+：

```bash
make build          # go build -trimpath -ldflags="-s -w"
go test ./internal/upstream ./internal/panel
```

`-s -w` 后二进制约 **6.7 MB**；不 strip 约 9.6 MB。

## 用法

```bash
# 一键 OAuth 登录 + 扫荡（默认登录完直接扫，不需要额外 --run）
tasksweep login --realm cn --auth-dir ./auths --ndjson

# 只登录，不扫荡
tasksweep login --realm cn --auth-dir ./auths --no-run

# 账号列表（不访问上游）——uid 从这里拿
tasksweep list --auth-dir /path/to/auths --json

# 查询任务（只读）
tasksweep tasks --all --json
tasksweep tasks --uid <uid> --include-mp --json

# 预览待办（只读，不执行）
tasksweep run --uid <uid> --dry-run --json

# 执行一键扫荡 + 自动领奖（有副作用）
tasksweep run --uid <uid> --json

# 多账号（默认串行；--workers 1 最安全）
tasksweep run --all --workers 1 --json

# 每账号一行 JSON，方便流式消费
tasksweep run --all --ndjson --quiet
```

通用参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--auth-dir` | `auths` | `workbuddy-*.json` 所在目录 |
| `--realm` | `cn` | `cn` / `global` / `all` |
| `--json` | 非 TTY 自动 | 强制 JSON |
| `--ndjson` | false | 每账号一行 |
| `--quiet` | false | 丢弃 stderr 日志 |
| `--uid` | 空 | 指定账号，可逗号分隔 |
| `--all` | false | 全部账号（`run` 必须给 `--uid` 或 `--all`） |
| `--dry-run` | false | 只列待办，不执行 |
| `--include-mp` | false | 合并小程序口径任务 |
| `--workers` | 1 | 账号间并发；建议 1 |
| `--timeout` | 5m | `login` 等待浏览器登录的超时 |
| `--no-run` | false | `login` 只登录不扫荡（默认登录后直接扫荡） |

## 输出示例

```json
{
  "ok": true,
  "command": "run",
  "generated_at": "2026-10-20T01:00:00+08:00",
  "accounts": [
    {
      "file": "auths/workbuddy-<uid>.json",
      "uid": "<uid>",
      "nickname": "副号",
      "realm": "cn",
      "status": "ok",
      "results": [
        {"task_code": "first_buddy", "status": "done", "message": "已领取"},
        {"task_code": "chat_5", "status": "done", "message": "进度 5/5"}
      ]
    }
  ]
}
```

`results[].status` 常见值：`done` / `skipped` / `error`。

## AI 自动化：timer + skill

推荐把“定时”和“AI 按需”分开：

- **日常定时**：systemd timer 每天跑 `tasksweep run --all --json`，结果写
  `~/.local/state/wb2api-tasks/last.json`；不依赖 agent 会话是否在运行。
- **AI 按需**：DSH skill `workbuddy-tasks` 负责“现在扫一次 / 看上次结果 / 处理登录 /
  诊断失败”，底层还是调同一个 CLI。

仓库里已有模板：

```bash
deploy/tasksweep.service
deploy/tasksweep.timer
skills/workbuddy-tasks/SKILL.md
```

安装 timer（确认 auth 目录里**只有副号**后再做）：

```bash
mkdir -p ~/.config/systemd/user ~/.local/state/wb2api-tasks
cp deploy/tasksweep.service deploy/tasksweep.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now tasksweep.timer
journalctl --user -u tasksweep.service -f
```

安装 skill（用户级）：

```bash
mkdir -p ~/.dsh/skills
ln -sfn ~/code/wb2api-tasks/skills/workbuddy-tasks ~/.dsh/skills/workbuddy-tasks
```

skill 只是给 agent 的操作说明，不会自己定时；定时必须靠 timer。

## uid 怎么拿

- `tasksweep list --json` 的 `accounts[].uid`；
- auth 文件名 `workbuddy-<uid>.json`；
- `tasksweep login` 的 `account.uid` / `auth_file`。

## 重要风险

- 任务动作会真实访问上游、上报行为事件、发真实对话、领取奖励；
- 有账号风控/封号风险，且上游协议随时可能变化；
- **一个账号只能由一个程序负责续期**。本 CLI 会按需刷新 token 并写回 auth 文件；
  如果同一个号同时被旧反代使用，可能互踢 refresh token；
- **只建议用副号跑**，不要拿主号当小白鼠；
- 不要把 `auths/`、token、面板 key 提交到仓库。

## 代码结构

```
cmd/tasksweep/        CLI（list/tasks/run/schema）
internal/panel/       任务动作实现（从原项目复制；新增 cli_export.go）
internal/upstream/    上游协议（任务/桌面事件链/领奖/对话）
internal/auth/        凭据解析、刷新、原子写回
internal/...          其余依赖（原样保留以复用任务代码）
```

当前 CLI 会编译整个 `internal/panel` 包，`-s -w` 后约 **6.7 MB**；后续可以把任务动作
拆到 `internal/tasks`，进一步瘦身，但行为不变。

## License

MIT。保留原项目版权声明，见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)。

原项目：https://github.com/linguo2625469/workbuddy2api-panel
