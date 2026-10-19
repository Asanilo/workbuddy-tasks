package panel

import "github.com/arinp/wb2api-tasks/internal/auth"

// RunAutoAllForCLI 是「一键完成全部可自动化任务」的稳定导出入口。
//
// 面板内部仍走 runAutoAll（HTTP handler 与任务中心共用同一套动作），
// CLI 只借用动作实现，不启动任何 HTTP 服务。返回的每个 map 是任务执行
// 结果（task_code / status / message），与面板「一键完成」的返回结构一致。
func (p *Panel) RunAutoAllForCLI(a *auth.Auth) []map[string]any {
	return p.runAutoAll(a)
}
