# deploy

两份 systemd user 模板，配合仓库根目录的 `tasksweep` 使用。

## 安装

```bash
mkdir -p ~/.config/systemd/user ~/.local/state/workbuddy-tasks
cp deploy/tasksweep.service deploy/tasksweep.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now tasksweep.timer
```

装之前先改 `tasksweep.service` 里的：

- `TASKSWEEP_AUTH_DIR`：只放副号的 auth 目录
- `TASKSWEEP_REALM`：`cn` 或 `global`
- `WorkingDirectory` / `ExecStart`：如果仓库不在 `~/code/workbuddy-tasks`

## 手动跑一次

```bash
systemctl --user start tasksweep.service
cat ~/.local/state/workbuddy-tasks/last.json
journalctl --user -u tasksweep.service -n 50 --no-pager
```

定时是每天 09:05（签到 09:00 之后），`Persistent=true` 会补跑错过的。
任务有风控风险，只建议跑副号。
