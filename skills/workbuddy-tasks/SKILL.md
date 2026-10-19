---
name: workbuddy-tasks
description: 运行 WorkBuddy 成长任务 CLI：OAuth 登录后直接扫荡、查询待办、诊断失败。当用户要扫任务、看还差哪些任务、处理登录或扫荡报错时使用。
---

# WorkBuddy 成长任务

使用独立 CLI `tasksweep`（仓库 `~/code/wb2api-tasks`），不要启动 wb2api HTTP 代理。

## 前置

```bash
BIN=~/code/wb2api-tasks/tasksweep
AUTH_DIR=~/app/wb2api-panel-v1.13.0-linux-amd64/auths
```

- 只对**副号**使用；不要在主号/主反代正在使用的号上跑。
- 一个账号只能由一个程序续期；不要和旧反代共用同一个 auth 文件。
- 先 `list` 拿 uid，或看 auth 文件名 `workbuddy-<uid>.json`。

## 命令

```bash
# 首次：OAuth 登录 + 立即扫荡（默认就扫，不需要 --run）
tasksweep login --realm cn --auth-dir "$AUTH_DIR" --ndjson

# 日常：已有 auth，直接扫
tasksweep run --all --auth-dir "$AUTH_DIR" --json

# 只预览待办
tasksweep run --all --auth-dir "$AUTH_DIR" --dry-run --json

# 查账号/uid
tasksweep list --auth-dir "$AUTH_DIR" --json

# 查任务列表（只读）
tasksweep tasks --all --auth-dir "$AUTH_DIR" --json
```

## AI 调用约定

- stdout 是 JSON；日志在 stderr。不要解析 stderr 当结果。
- `login` 用 `--ndjson`：先读 `{"event":"login_url"}`，把 URL 给用户打开；之后会有
  `login_ok`、`sweep` 或 `error`。超时默认 5 分钟，可用 `--timeout 10m`。
- `run` 前先 `--dry-run --json`，看 `accounts[].pending` 是否为空；空就无需执行。
- 解析顶层 `ok`、`accounts[].status`、`accounts[].results[].status`（done/skipped/error）。
- 退出码：0 全成功 / 1 部分失败 / 2 用法 / 3 无账号 / 4 上游。
- 不要自动重试登录；登录需要用户在浏览器完成。不要循环刷 `run`，失败先看 `error`。

## 定时任务（推荐给日常）

不要依赖 agent 会话做定时。用 systemd timer 跑：

```bash
tasksweep run --all --auth-dir "$AUTH_DIR" --realm cn --json --quiet \
  > ~/.local/state/wb2api-tasks/last.json 2>> ~/.local/state/wb2api-tasks/last.err
```

模板在仓库 `deploy/tasksweep.service` / `deploy/tasksweep.timer`。安装前先确认：
- auth 目录里只有副号；
- 主反代没有使用同一个号；
- 先手动跑一次 dry-run。

agent 可以用 skill 做“按需扫一次/查上次结果/处理登录”，定时交给 timer。

## 风险

任务动作会真实上报行为、发真实对话、领奖，有风控/封号风险。只用副号；
失败时不要连续重试；不要提交 auths/token 到仓库。
