package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asanilo/workbuddy-tasks/internal/auth"
	"github.com/asanilo/workbuddy-tasks/internal/panel"
	"github.com/asanilo/workbuddy-tasks/internal/upstream"
)

const (
	loginBaseCN       = "https://copilot.tencent.com"
	loginBaseGlobal   = "https://www.workbuddy.ai"
	loginOriginCN     = "https://www.codebuddy.cn"
	loginOriginGlobal = "https://www.workbuddy.ai"
	loginClientUA     = "CLI/2.63.2 CodeBuddy/2.63.2"
)

type loginToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	Domain       string `json:"domain"`
}

type loginAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
	Realm        string `json:"realm,omitempty"`
}

type loginEnvelope struct {
	OK       bool           `json:"ok"`
	Command  string         `json:"command"`
	Realm    string         `json:"realm"`
	LoginURL string         `json:"login_url,omitempty"`
	Account  *loginAccount  `json:"account,omitempty"`
	AuthFile string         `json:"auth_file,omitempty"`
	Sweep    *AccountReport `json:"sweep,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func runLogin(cf *commonFlags, timeout time.Duration, runSweep bool) error {
	realm := cf.realm
	if realm != "cn" && realm != "global" {
		return fmt.Errorf("login --realm must be cn or global")
	}
	base, origin := loginBaseCN, loginOriginCN
	if realm == "global" {
		base, origin = loginBaseGlobal, loginOriginGlobal
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	headers := loginHeaders(origin)

	// 1) state + auth URL
	data, _, err := loginJSON(client, http.MethodPost, base+"/v2/plugin/auth/state?platform=CLI", headers, []byte("{}"))
	if err != nil {
		return fmt.Errorf("auth state: %w", err)
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		return fmt.Errorf("auth state: missing state or authUrl")
	}

	emitLoginURL(cf, st.AuthURL, realm)

	// 2) 自动 poll，不再等用户按 y；用户只需在浏览器完成登录
	deadline := time.Now().Add(timeout)
	var tok loginToken
	for {
		raw, status, err := loginJSON(client, http.MethodGet, base+"/v2/plugin/auth/token?state="+st.State, headers, nil)
		if err == nil {
			if json.Unmarshal(raw, &tok) == nil && tok.AccessToken != "" {
				break
			}
		} else if status == 0 || status >= 500 {
			return fmt.Errorf("token endpoint: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("login timeout after %s（未在浏览器完成登录？）", timeout)
		}
		time.Sleep(2 * time.Second)
	}

	// 3) account 信息（可选；uid 必须有）
	var acct loginAccount
	acctHeaders := func(r *http.Request) {
		headers(r)
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if raw, _, err := loginJSON(client, http.MethodGet, base+"/v2/plugin/login/account?state="+st.State, acctHeaders, nil); err == nil {
		_ = json.Unmarshal(raw, &acct)
	}
	if strings.TrimSpace(acct.UID) == "" {
		return fmt.Errorf("login succeeded but uid is empty")
	}
	acct.Realm = auth.ResolveRealm(realm, tok.Domain)
	if acct.Realm == "" {
		acct.Realm = realm
	}
	expiresIn := tok.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Unix() + expiresIn

	// 4) 落盘 auths/workbuddy-<uid>.json（与 internal/auth.Parse 兼容）
	if err := os.MkdirAll(cf.authDir, 0o700); err != nil {
		return fmt.Errorf("mkdir auth dir: %w", err)
	}
	authFile := filepath.Join(cf.authDir, "workbuddy-"+acct.UID+".json")
	doc := map[string]any{
		"account": map[string]any{
			"uid":          acct.UID,
			"enterpriseId": acct.EnterpriseID,
			"nickname":     acct.Nickname,
		},
		"auth": map[string]any{
			"accessToken":  tok.AccessToken,
			"refreshToken": tok.RefreshToken,
			"expiresAt":    expiresAt,
			"domain":       tok.Domain,
			"realm":        acct.Realm,
		},
	}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(authFile, raw, 0o600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	a, err := auth.Parse(raw)
	if err != nil {
		return fmt.Errorf("reparse auth: %w", err)
	}
	a.FilePath = authFile

	// 5) 可选：登录后直接扫荡
	var sweep *AccountReport
	if runSweep {
		up := upstream.New()
		pn := panel.New(panel.Config{Upstream: up, Version: "tasksweep"})
		rep := runOne(up, pn, a, false, false)
		sweep = &rep
	}

	emitLoginOK(cf, &acct, st.AuthURL, authFile, sweep)
	if sweep != nil && sweep.Status != "ok" {
		return fmt.Errorf("sweep: %s", sweep.Error)
	}
	return nil
}

func emitLoginURL(cf *commonFlags, url, realm string) {
	if cf.ndjson {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"event": "login_url", "url": url, "realm": realm,
		})
		return
	}
	// 非 ndjson：URL 走 stderr，立即让用户看到；stdout 留最终 JSON。
	fmt.Fprintf(os.Stderr, "请打开以下链接完成 %s 登录：\n\n  %s\n\n等待登录完成…\n", realm, url)
}

func emitLoginOK(cf *commonFlags, acct *loginAccount, loginURL, authFile string, sweep *AccountReport) {
	env := loginEnvelope{
		OK: true, Command: "login", Realm: acct.Realm, LoginURL: loginURL,
		Account: acct, AuthFile: authFile, Sweep: sweep,
	}
	if cf.ndjson {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"event": "login_ok", "account": acct, "auth_file": authFile,
		})
		if sweep != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"event": "sweep", "sweep": sweep,
			})
		}
		return
	}
	if asJSON(cf) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(env)
		return
	}
	fmt.Printf("登录成功 uid=%s nickname=%s realm=%s\n已保存: %s\n", acct.UID, acct.Nickname, acct.Realm, authFile)
	if sweep != nil {
		fmt.Printf("扫荡: status=%s results=%d\n", sweep.Status, len(sweep.Results))
	}
}

func asJSON(cf *commonFlags) bool {
	if cf.json {
		return true
	}
	fi, err := os.Stdout.Stat()
	return err != nil || fi.Mode()&os.ModeCharDevice == 0
}

func loginHeaders(origin string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("User-Agent", loginClientUA)
	}
}

func loginJSON(client *http.Client, method, url string, headers func(*http.Request), body []byte) (json.RawMessage, int, error) {
	var rdr *strings.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, 0, err
	}
	headers(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&envelope); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("http %d: %s", resp.StatusCode, envelope.Msg)
	}
	if envelope.Code != 0 {
		return nil, resp.StatusCode, fmt.Errorf("code=%d: %s", envelope.Code, envelope.Msg)
	}
	return envelope.Data, resp.StatusCode, nil
}

func emitLoginError(cf *commonFlags, err error) {
	if cf.ndjson {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"event": "error", "error": err.Error()})
		return
	}
	if asJSON(cf) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(loginEnvelope{OK: false, Command: "login", Error: err.Error()})
		return
	}
	fmt.Fprintf(os.Stderr, "login: %v\n", err)
}
