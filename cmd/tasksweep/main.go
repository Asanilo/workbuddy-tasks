// tasksweep 是 wb2api 任务体系的独立 CLI：不启动 HTTP 代理，只做
// 「读 auths → 查询/接受/执行/领取成长任务 → 输出结构化结果」。
//
// 设计目标（AI 友好）：
//   - 非交互：所有输入走 flag，绝不等待 stdin；
//   - 结构化输出：默认在非 TTY 下输出 JSON；--json 强制 JSON；
//   - 稳定退出码：0 全成功 / 1 部分失败 / 2 用法错误 / 3 无可用账号 / 4 上游错误；
//   - dry-run：先看要做哪些任务，再决定是否真跑；
//   - 日志全部走 stderr，stdout 只放结果，方便 agent 解析。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asanilo/workbuddy-tasks/internal/auth"
	"github.com/asanilo/workbuddy-tasks/internal/panel"
	"github.com/asanilo/workbuddy-tasks/internal/upstream"
)

const (
	exitOK       = 0
	exitPartial  = 1
	exitUsage    = 2
	exitNoAuth   = 3
	exitUpstream = 4
)

type commonFlags struct {
	authDir string
	json    bool
	ndjson  bool
	quiet   bool
	realm   string
}

type AccountReport struct {
	File         string           `json:"file"`
	UID          string           `json:"uid"`
	Nickname     string           `json:"nickname,omitempty"`
	Realm        string           `json:"realm"`
	Status       string           `json:"status"` // ok | error | skipped
	Error        string           `json:"error,omitempty"`
	ExpiresAt    int64            `json:"expires_at,omitempty"`
	NeedsRefresh bool             `json:"needs_refresh,omitempty"`
	Tasks        []upstream.Task  `json:"tasks,omitempty"`
	Pending      []upstream.Task  `json:"pending,omitempty"`
	Results      []map[string]any `json:"results,omitempty"`
	Credits      *CreditSummary   `json:"credits,omitempty"`
}

// CreditBucket 某自然日（UTC+8）到期的剩余积分合计。
type CreditBucket struct {
	Date   string `json:"date"`
	Remain int64  `json:"remain"`
}

// CreditSummary 账号积分概览：总剩余/总量、临期窗口、逐日到期批次与可选逐包明细。
type CreditSummary struct {
	Remain       int64                    `json:"remain"`
	Total        int64                    `json:"total"`
	SoonDays     int                      `json:"soon_days"`
	SoonRemain   int64                    `json:"soon_remain"`
	Earliest     string                   `json:"earliest,omitempty"`      // 最近到期日（YYYY-MM-DD）
	EarliestDays int                      `json:"earliest_days,omitempty"` // 距今天数
	Buckets      []CreditBucket           `json:"buckets,omitempty"`
	Packages     []upstream.CreditPackage `json:"packages,omitempty"`
}

type Envelope struct {
	OK          bool            `json:"ok"`
	Command     string          `json:"command"`
	GeneratedAt string          `json:"generated_at"`
	Accounts    []AccountReport `json:"accounts"`
	Errors      []string        `json:"errors,omitempty"`
}

func main() {
	log.SetOutput(os.Stderr)
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	cf := &commonFlags{}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cf.authDir, "auth-dir", envOr("TASKSWEEP_AUTH_DIR", "auths"), "账号凭据目录（workbuddy-*.json；可用 TASKSWEEP_AUTH_DIR）")
	fs.BoolVar(&cf.json, "json", false, "强制 JSON 输出（非 TTY 默认开启）")
	fs.BoolVar(&cf.ndjson, "ndjson", false, "每个账号一行 JSON")
	fs.BoolVar(&cf.quiet, "quiet", false, "丢弃日志（stdout 仍输出结果）")
	fs.StringVar(&cf.realm, "realm", envOr("TASKSWEEP_REALM", "cn"), "账号域：cn | global | all（可用 TASKSWEEP_REALM）")

	var uid string
	var all, dryRun, includeMP bool
	var workers int
	var loginTimeout time.Duration
	var loginNoRun bool
	var soonDays int
	var showPackages bool
	switch cmd {
	case "list":
		fs.StringVar(&uid, "uid", "", "只列指定 UID（可逗号分隔）")
		fs.BoolVar(&all, "all", true, "列出全部（默认）")
	case "tasks":
		fs.StringVar(&uid, "uid", "", "只查指定 UID（可逗号分隔）")
		fs.BoolVar(&all, "all", false, "查询全部账号")
		fs.BoolVar(&includeMP, "include-mp", false, "合并小程序口径任务（可能多出 mp 专属任务）")
	case "credits":
		fs.StringVar(&uid, "uid", "", "只查指定 UID（可逗号分隔）")
		fs.BoolVar(&all, "all", true, "查询全部账号（默认）")
		fs.IntVar(&soonDays, "soon", 7, "临期窗口天数（默认 7）")
		fs.BoolVar(&showPackages, "packages", false, "输出逐包明细（默认只输出汇总与到期批次）")
	case "run":
		fs.StringVar(&uid, "uid", "", "只跑指定 UID（可逗号分隔）")
		fs.BoolVar(&all, "all", false, "跑全部账号")
		fs.BoolVar(&dryRun, "dry-run", false, "只列出待办，不执行任何动作")
		fs.BoolVar(&includeMP, "include-mp", false, "dry-run 时同时合并 mp 口径任务")
		fs.IntVar(&workers, "workers", 1, "账号间并发数（默认 1，建议 1）")
	case "login":
		fs.DurationVar(&loginTimeout, "timeout", 5*time.Minute, "等待浏览器登录完成的超时")
		fs.BoolVar(&loginNoRun, "no-run", false, "只登录，不执行扫荡（默认登录后直接扫荡）")
	case "schema":
		// no extra flags
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(exitUsage)
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(exitUsage)
	}
	if cf.quiet {
		log.SetOutput(io.Discard)
	}
	if cf.realm != "cn" && cf.realm != "global" && cf.realm != "all" {
		fmt.Fprintf(os.Stderr, "invalid --realm %q (want cn|global|all)\n", cf.realm)
		os.Exit(exitUsage)
	}
	if cmd == "run" && !all && strings.TrimSpace(uid) == "" {
		fmt.Fprintln(os.Stderr, "run requires --uid or --all")
		os.Exit(exitUsage)
	}
	if cmd == "tasks" && !all && strings.TrimSpace(uid) == "" {
		fmt.Fprintln(os.Stderr, "tasks requires --uid or --all")
		os.Exit(exitUsage)
	}

	if cmd == "schema" {
		printSchema()
		return
	}
	if cmd == "login" {
		if err := runLogin(cf, loginTimeout, !loginNoRun); err != nil {
			emitLoginError(cf, err)
			os.Exit(exitPartial)
		}
		return
	}

	accts, err := loadAccounts(cf.authDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load auths: %v\n", err)
		os.Exit(exitNoAuth)
	}
	accts = filterAccounts(accts, uid, all, cf.realm)
	if len(accts) == 0 {
		fmt.Fprintf(os.Stderr, "no accounts selected (auth-dir=%s, uid=%q, realm=%s)\n", cf.authDir, uid, cf.realm)
		os.Exit(exitNoAuth)
	}

	up := upstream.New()
	env := Envelope{Command: cmd, GeneratedAt: time.Now().Format(time.RFC3339)}
	partial := false

	switch cmd {
	case "list":
		for _, a := range accts {
			env.Accounts = append(env.Accounts, AccountReport{
				File: a.FilePath, UID: a.UID, Nickname: a.Nickname, Realm: a.Realm(),
				Status: "ok", ExpiresAt: a.ExpiresAt, NeedsRefresh: a.NeedsRefresh(2 * time.Hour),
			})
		}
	case "tasks":
		for _, a := range accts {
			rep := AccountReport{File: a.FilePath, UID: a.UID, Nickname: a.Nickname, Realm: a.Realm()}
			if err := ensureToken(up, a); err != nil {
				rep.Status, rep.Error = "error", "refresh: "+err.Error()
				partial = true
			} else if tasks, err := listTasks(up, a, includeMP); err != nil {
				rep.Status, rep.Error = "error", err.Error()
				partial = true
			} else {
				rep.Status, rep.Tasks = "ok", tasks
			}
			env.Accounts = append(env.Accounts, rep)
		}
	case "credits":
		for _, a := range accts {
			rep := AccountReport{File: a.FilePath, UID: a.UID, Nickname: a.Nickname, Realm: a.Realm()}
			if err := ensureToken(up, a); err != nil {
				rep.Status, rep.Error = "error", "refresh: "+err.Error()
				partial = true
			} else if packs, remain, total, err := up.CreditPackages(a); err != nil {
				rep.Status, rep.Error = "error", err.Error()
				partial = true
			} else {
				rep.Status = "ok"
				rep.Credits = summarizeCredits(packs, remain, total, soonDays, showPackages)
			}
			env.Accounts = append(env.Accounts, rep)
		}
	case "run":
		if workers < 1 {
			workers = 1
		}
		if workers > len(accts) {
			workers = len(accts)
		}
		type job struct {
			idx int
			a   *auth.Auth
		}
		jobs := make(chan job)
		reports := make([]AccountReport, len(accts))
		done := make(chan struct{})
		for w := 0; w < workers; w++ {
			go func() {
				pn := panel.New(panel.Config{Upstream: up, Version: "tasksweep"})
				for j := range jobs {
					reports[j.idx] = runOne(up, pn, j.a, dryRun, includeMP)
				}
				done <- struct{}{}
			}()
		}
		for i, a := range accts {
			jobs <- job{i, a}
		}
		close(jobs)
		for w := 0; w < workers; w++ {
			<-done
		}
		env.Accounts = reports
		for _, r := range reports {
			if r.Status != "ok" {
				partial = true
			}
		}
	}

	env.OK = !partial
	if cf.ndjson {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		for _, a := range env.Accounts {
			_ = enc.Encode(a)
		}
	} else {
		writeOutput(cf, env)
	}
	if partial {
		os.Exit(exitPartial)
	}
}

func runOne(up *upstream.Client, pn *panel.Panel, a *auth.Auth, dryRun, includeMP bool) AccountReport {
	rep := AccountReport{File: a.FilePath, UID: a.UID, Nickname: a.Nickname, Realm: a.Realm()}
	if err := ensureToken(up, a); err != nil {
		rep.Status, rep.Error = "error", "refresh: "+err.Error()
		return rep
	}
	if dryRun {
		tasks, err := listTasks(up, a, includeMP)
		if err != nil {
			rep.Status, rep.Error = "error", err.Error()
			return rep
		}
		rep.Status = "ok"
		for _, t := range tasks {
			if !t.Claimed && !t.Locked && (t.Target == 0 || t.Current < t.Target) {
				rep.Pending = append(rep.Pending, t)
			}
		}
		return rep
	}
	rep.Status = "ok"
	rep.Results = pn.RunAutoAllForCLI(a)
	for _, r := range rep.Results {
		if s, _ := r["status"].(string); s == "error" {
			rep.Status = "error"
			if msg, _ := r["message"].(string); msg != "" && rep.Error == "" {
				rep.Error = msg
			}
		}
	}
	return rep
}

// creditZone 上游到期口径固定 UTC+8——避免进程 TZ 影响「还有几天到期」。
var creditZone = time.FixedZone("UTC+8", 8*60*60)

// summarizeCredits 把逐包明细收敛成「剩余/总量 + 临期窗口 + 逐日到期批次」。
//
// 口径与面板一致：只统计剩余 > 0 且到期时刻在未来的包；按自然日（UTC+8）聚合；
// soonDays 内到期的剩余量计入 soon_remain。withPackages 为 true 时附逐包明细。
func summarizeCredits(packs []upstream.CreditPackage, remain, total int64, soonDays int, withPackages bool) *CreditSummary {
	if soonDays < 0 {
		soonDays = 0
	}
	now := time.Now().In(creditZone)
	soonEnd := now.Add(time.Duration(soonDays) * 24 * time.Hour)
	sum := &CreditSummary{Remain: remain, Total: total, SoonDays: soonDays}
	bucketRemain := make(map[string]int64, 4)
	var earliest time.Time
	for _, p := range packs {
		if p.Remain <= 0 {
			continue
		}
		if withPackages {
			sum.Packages = append(sum.Packages, p)
		}
		if p.ExpiresAt <= 0 {
			continue
		}
		end := time.UnixMilli(p.ExpiresAt).In(creditZone)
		if !end.After(now) {
			continue
		}
		if earliest.IsZero() || end.Before(earliest) {
			earliest = end
		}
		if !end.After(soonEnd) {
			sum.SoonRemain += p.Remain
		}
		bucketRemain[end.Format("2006-01-02")] += p.Remain
	}
	if withPackages {
		sort.SliceStable(sum.Packages, func(i, j int) bool {
			a, b := sum.Packages[i], sum.Packages[j]
			if a.ExpiresAt != b.ExpiresAt {
				return a.ExpiresAt < b.ExpiresAt
			}
			return a.Name < b.Name
		})
	}
	if !earliest.IsZero() {
		sum.Earliest = earliest.Format("2006-01-02")
		sum.EarliestDays = daysUntil(now, earliest)
	}
	days := make([]string, 0, len(bucketRemain))
	for d := range bucketRemain {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		sum.Buckets = append(sum.Buckets, CreditBucket{Date: d, Remain: bucketRemain[d]})
	}
	return sum
}

// daysUntil 返回 from 到 to 的自然日差（按各自所在时区的日期计算），不受具体时刻影响。
func daysUntil(from, to time.Time) int {
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	start := time.Date(fy, fm, fd, 0, 0, 0, 0, from.Location())
	end := time.Date(ty, tm, td, 0, 0, 0, 0, to.Location())
	return int(end.Sub(start).Hours() / 24)
}

func ensureToken(up *upstream.Client, a *auth.Auth) error {
	if !a.NeedsRefresh(2 * time.Hour) {
		return nil
	}
	if err := up.RefreshToken(a); err != nil {
		return err
	}
	return a.SaveAtomic()
}

func listTasks(up *upstream.Client, a *auth.Auth, includeMP bool) ([]upstream.Task, error) {
	tasks, err := up.ListTasks(a)
	if err != nil {
		return nil, err
	}
	if !includeMP {
		return tasks, nil
	}
	mp, err := up.ListTasksMP(a)
	if err != nil {
		return tasks, nil // mp 失败不阻塞默认口径
	}
	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		seen[t.TaskCode] = true
	}
	for _, t := range mp {
		if !seen[t.TaskCode] {
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

func loadAccounts(dir string) ([]*auth.Auth, error) {
	files, err := filepath.Glob(filepath.Join(dir, "workbuddy-*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []*auth.Auth
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		a, err := auth.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		a.FilePath = f
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no workbuddy-*.json in %s", dir)
	}
	return out, nil
}

func filterAccounts(in []*auth.Auth, uid string, all bool, realm string) []*auth.Auth {
	uidSet := map[string]bool{}
	for _, s := range strings.Split(uid, ",") {
		if s = strings.TrimSpace(s); s != "" {
			uidSet[s] = true
		}
	}
	var out []*auth.Auth
	for _, a := range in {
		if len(uidSet) > 0 && !uidSet[a.UID] {
			continue
		}
		if realm != "all" && a.Realm() != realm {
			continue
		}
		out = append(out, a)
	}
	return out
}

func writeOutput(cf *commonFlags, env Envelope) {
	asJSON := cf.json
	if !asJSON {
		if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			asJSON = true
		}
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(env)
		return
	}
	for _, a := range env.Accounts {
		fmt.Printf("%s\t%s\t%s\t%s", a.UID, a.Nickname, a.Realm, a.Status)
		if a.Error != "" {
			fmt.Printf("\t%s", a.Error)
		}
		if len(a.Pending) > 0 {
			fmt.Printf("\tpending=%d", len(a.Pending))
		}
		if len(a.Results) > 0 {
			ok, bad := 0, 0
			for _, r := range a.Results {
				if s, _ := r["status"].(string); s == "error" {
					bad++
				} else if s == "done" {
					ok++
				}
			}
			fmt.Printf("\tdone=%d\terror=%d", ok, bad)
		}
		fmt.Println()
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func usage() {
	fmt.Fprintf(os.Stderr, `tasksweep — WorkBuddy 成长任务 CLI（AI 友好）

用法:
  tasksweep <command> [flags]

命令:
  list     列出账号（不访问上游）
  login    OAuth 一键登录 + 扫荡（默认登录后直接跑；--no-run 只登录）
  tasks    查询任务列表（可 --include-mp）
  credits  查询积分与逐包到期（只读）
  run      执行一键扫荡/领奖（--dry-run 只看待办）
  schema   输出 JSON 输出结构说明

通用 flags:
  --auth-dir DIR   凭据目录，默认 auths
  --realm cn|global|all  账号域，默认 cn
  --json           强制 JSON（非 TTY 默认 JSON）
  --ndjson         每个账号一行 JSON
  --quiet          丢弃 stderr 日志

tasks/run flags:
  --uid UID[,UID]  指定账号
  --all            全部账号（run 必须显式给 --all 或 --uid）
  --dry-run        只列待办，不执行（run）
  --include-mp     合并小程序口径任务
  --workers N      账号间并发，默认 1

credits flags:
  --uid UID[,UID]  指定账号（默认全部）
  --soon N         临期窗口天数，默认 7
  --packages       输出逐包明细（默认只给汇总与到期批次）

login flags:
  --realm cn|global  登录版本（默认 cn）
  --timeout 5m       等待浏览器登录完成
  --no-run           只登录，不扫荡（默认登录后直接扫荡）
  --ndjson           流式输出 login_url / login_ok / sweep 事件

退出码:
  0 全成功  1 部分失败  2 用法错误  3 无可用账号  4 上游错误

示例:
  tasksweep login --realm cn --auth-dir ./auths --ndjson
  tasksweep list --auth-dir ./auths --json
  tasksweep tasks --all --json
  tasksweep credits --all --json
  tasksweep credits --uid <uid> --soon 7 --packages --json
  tasksweep run --uid <uid> --dry-run --json
  tasksweep run --all --workers 1 --json

注意: 任务动作会真实访问上游并改变账号状态；建议只用副号。
`)
}

func printSchema() {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok":           map[string]any{"type": "boolean"},
			"command":      map[string]any{"type": "string"},
			"generated_at": map[string]any{"type": "string"},
			"accounts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file":          map[string]any{"type": "string"},
						"uid":           map[string]any{"type": "string"},
						"nickname":      map[string]any{"type": "string"},
						"realm":         map[string]any{"type": "string"},
						"status":        map[string]any{"type": "string", "enum": []string{"ok", "error", "skipped"}},
						"error":         map[string]any{"type": "string"},
						"expires_at":    map[string]any{"type": "integer"},
						"needs_refresh": map[string]any{"type": "boolean"},
						"tasks":         map[string]any{"type": "array"},
						"pending":       map[string]any{"type": "array"},
						"results":       map[string]any{"type": "array"},
						"credits": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"remain":        map[string]any{"type": "integer"},
								"total":         map[string]any{"type": "integer"},
								"soon_days":     map[string]any{"type": "integer"},
								"soon_remain":   map[string]any{"type": "integer"},
								"earliest":      map[string]any{"type": "string"},
								"earliest_days": map[string]any{"type": "integer"},
								"buckets": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"date":   map[string]any{"type": "string"},
											"remain": map[string]any{"type": "integer"},
										},
									},
								},
								"packages": map[string]any{"type": "array"},
							},
						},
					},
				},
			},
			"errors": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
	b, _ := json.MarshalIndent(schema, "", "  ")
	fmt.Println(string(b))
}
