package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// WXBizMsgCrypt 实现企业微信「接收消息」的加解密协议（WXBizMsgCrypt 标准）。
//
// 企业微信在应用后台配置「接收消息」后会生成 Token + EncodingAESKey，
// 发给本服务的消息体是 AES-256-CBC 加密的 XML，响应也要加密回传。
type WXBizMsgCrypt struct {
	Token     string
	ReceiveID string // corpID
	aesKey    []byte // 32 字节
}

// NewWXBizMsgCrypt 校验 EncodingAESKey（43 位 base64 → 32 字节）并构造。
func NewWXBizMsgCrypt(token, encodingAESKey, corpID string) (*WXBizMsgCrypt, error) {
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodingAESKey) + "=")
	if err != nil {
		return nil, fmt.Errorf("EncodingAESKey 不是合法 base64: %v", err)
	}
	if len(dec) != 32 {
		return nil, fmt.Errorf("EncodingAESKey 解码后应为 32 字节（43 位），实际 %d", len(dec))
	}
	return &WXBizMsgCrypt{Token: token, ReceiveID: corpID, aesKey: dec}, nil
}

// Sign 计算 SHA1 签名：token、timestamp、nonce、data 四串排序拼接。
func (c *WXBizMsgCrypt) Sign(timestamp, nonce, data string) string {
	arr := []string{c.Token, timestamp, nonce, data}
	sort.Strings(arr)
	h := sha1.Sum([]byte(strings.Join(arr, "")))
	return fmt.Sprintf("%x", h)
}

// Encrypt 加密明文为 base64 密文（16 随机 + 4 长度 + 明文 + corpid）。
func (c *WXBizMsgCrypt) Encrypt(plain string) (string, error) {
	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return "", err
	}
	buf := new(bytes.Buffer)
	rnd := make([]byte, 16)
	if _, err := rand.Read(rnd); err != nil {
		return "", err
	}
	buf.Write(rnd)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(plain)))
	buf.Write(lenBuf[:])
	buf.WriteString(plain)
	buf.WriteString(c.ReceiveID)
	padded := cbcPad(buf.Bytes(), block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, c.aesKey[:16]).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 解密 base64 密文，校验 corpid，返回明文。
func (c *WXBizMsgCrypt) Decrypt(b64 string) (string, error) {
	cipherBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return "", err
	}
	if len(cipherBytes) == 0 || len(cipherBytes)%block.BlockSize() != 0 {
		return "", fmt.Errorf("密文长度不合法")
	}
	out := make([]byte, len(cipherBytes))
	cipher.NewCBCDecrypter(block, c.aesKey[:16]).CryptBlocks(out, cipherBytes)
	out = cbcUnpad(out)
	if len(out) < 20 {
		return "", fmt.Errorf("解密数据过短")
	}
	msgLen := int(binary.BigEndian.Uint32(out[16:20]))
	if 20+msgLen > len(out) {
		return "", fmt.Errorf("解密数据损坏")
	}
	receiveid := string(out[20+msgLen:])
	if c.ReceiveID != "" && receiveid != c.ReceiveID {
		return "", fmt.Errorf("corpid 不匹配")
	}
	return string(out[20 : 20+msgLen]), nil
}

// encryptedResponse 生成带签名的加密响应体。
func (c *WXBizMsgCrypt) encryptedResponse(plain, timestamp, nonce string) (string, error) {
	enc, err := c.Encrypt(plain)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt><MsgSig><![CDATA[%s]]></MsgSig><TimeStamp>%s</TimeStamp><Nonce><![CDATA[%s]]></Nonce></xml>",
		enc, c.Sign(timestamp, nonce, enc), timestamp, nonce), nil
}

func cbcPad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func cbcUnpad(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	if pad := int(b[len(b)-1]); pad > 0 && pad <= len(b) {
		return b[:len(b)-pad]
	}
	return b
}

// weComEncryptReq 加密模式下企业微信发来的请求体。
type weComEncryptReq struct {
	XMLName xml.Name `xml:"xml"`
	Encrypt string   `xml:"Encrypt"`
	AgentID string   `xml:"AgentID"`
	EncSig  string   `xml:"EncSig"`
}

// weComTextMsg 解密后的文本消息。
type weComTextMsg struct {
	XMLName  xml.Name `xml:"xml"`
	FromUser string   `xml:"FromUserName"`
	ToUser   string   `xml:"ToUserName"`
	MsgType  string   `xml:"MsgType"`
	Content  string   `xml:"Content"`
	Event    string   `xml:"Event"`
	EventKey string   `xml:"EventKey"`
}

// textReplyXML 构造明文被动回复 XML。
func textReplyXML(toUser, fromUser, content string) string {
	return fmt.Sprintf("<xml><ToUserName><![CDATA[%s]]></ToUserName><FromUserName><![CDATA[%s]]></FromUserName><CreateTime>%d</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[%s]]></Content></xml>",
		toUser, fromUser, time.Now().Unix(), content)
}

// containsWeComUser 判断发送者是否在白名单（逗号分隔，* 或空表示不限制）。
func containsWeComUser(whitelist, fromUser string) bool {
	whitelist = strings.TrimSpace(whitelist)
	if whitelist == "" || strings.Contains(whitelist, "*") {
		return true
	}
	for _, u := range strings.Split(whitelist, ",") {
		if strings.TrimSpace(u) == fromUser {
			return true
		}
	}
	return false
}

// handleWeComCallback 是企业微信「接收消息」的入口，公开路由（不做 requireAuth）。
// GET 处理首次配置的 URL 验证（回显解密后的 echostr）；POST 处理文本消息。
func (s *Server) handleWeComCallback(w http.ResponseWriter, r *http.Request) {
	ns := s.notify.Current()
	if !ns.CallbackEnabled {
		logf("wecom 回调未启用，拒绝 %s %s", r.Method, r.URL.Path)
		http.Error(w, "callback disabled", http.StatusForbidden)
		return
	}
	crypt, err := NewWXBizMsgCrypt(strings.TrimSpace(ns.CallbackToken), strings.TrimSpace(ns.CallbackAESKey), strings.TrimSpace(ns.CorpID))
	if err != nil {
		logf("wecom 回调配置错误: %v", err)
		http.Error(w, "bad callback config: "+err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	switch r.Method {
	case http.MethodGet:
		sig, ts, nonce := q.Get("msg_signature"), q.Get("timestamp"), q.Get("nonce")
		echostr := q.Get("echostr")
		logf("wecom GET 验证: ts=%s nonce=%s 签名匹配=%v echostr_len=%d", ts, nonce, crypt.Sign(ts, nonce, echostr) == sig, len(echostr))
		if crypt.Sign(ts, nonce, echostr) != sig {
			http.Error(w, "bad signature", http.StatusForbidden)
			return
		}
		plain, err := crypt.Decrypt(echostr)
		if err != nil {
			logf("wecom GET 解密 echostr 失败: %v", err)
			http.Error(w, "decrypt failed", http.StatusBadRequest)
			return
		}
		logf("wecom GET 验证成功，回显 %d 字", len(plain))
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, plain)
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var req weComEncryptReq
		if err := xml.Unmarshal(body, &req); err != nil {
			logf("wecom POST 解析 XML 失败: %v", err)
			http.Error(w, "bad xml body", http.StatusBadRequest)
			return
		}
		ts, nonce, sig := q.Get("timestamp"), q.Get("nonce"), q.Get("msg_signature")
		logf("wecom POST 收到（密文 %d 字）签名匹配=%v ts=%s nonce=%s", len(req.Encrypt), crypt.Sign(ts, nonce, req.Encrypt) == sig, ts, nonce)
		if crypt.Sign(ts, nonce, req.Encrypt) != sig {
			http.Error(w, "bad signature", http.StatusForbidden)
			return
		}
		plainXML, err := crypt.Decrypt(req.Encrypt)
		if err != nil {
			logf("wecom POST 解密失败: %v", err)
			http.Error(w, "decrypt failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		var msg weComTextMsg
		if err := xml.Unmarshal([]byte(plainXML), &msg); err != nil {
			logf("wecom 解析消息失败: %v（明文前100: %.100s）", err, plainXML)
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		logf("wecom 收到消息 from=%s msgtype=%s content=%q", msg.FromUser, msg.MsgType, msg.Content)
		if !containsWeComUser(ns.CallbackOwner, msg.FromUser) {
			logf("wecom 白名单拒绝 from=%s whitelist=%q", msg.FromUser, ns.CallbackOwner)
			http.Error(w, "forbidden: sender not in whitelist", http.StatusForbidden)
			return
		}
		// 菜单点击（click 类型）推的是 event 消息，EventKey 决定执行什么，
		// 这样点完菜单直接在聊天里出结果，不用跳 H5 页面。
		cmd := strings.TrimSpace(msg.Content)
		if strings.EqualFold(msg.MsgType, "event") {
			switch strings.ToUpper(strings.TrimSpace(msg.EventKey)) {
			case "STATUS":
				cmd = "状态"
			case "RETRY_FAILED":
				cmd = "重试失败"
			case "RUN_ALL":
				cmd = "全部重试"
			case "FAILED_LIST":
				cmd = "失败列表"
			case "IMPORT_SITES":
				cmd = "站点导入"
			case "COOKIE_SYNC":
				cmd = "cookie 更新"
			case "SYNC_SITES_TABLE":
				cmd = "站点名表同步"
			case "SCHEDULE_ON":
				cmd = "恢复定时"
			case "SCHEDULE_OFF":
				cmd = "暂停定时"
			case "RETRY_HINT":
				cmd = "怎么重试单站"
			case "HELP":
				cmd = "帮助"
			default:
				cmd = ""
			}
			logf("wecom 菜单点击 event=%s key=%s → 命令=%q", msg.Event, msg.EventKey, cmd)
		}
		reply := s.dispatchWeComCommand(cmd)
		logf("wecom 执行完成 from=%s: %q", msg.FromUser, reply)

		// 企微对 event 消息的被动回复常常不显示（实测点了菜单没反应，
		// 但服务端日志显示回复已生成）。改为：立即回空串 ACK 结束本次回调，
		// 再用应用消息 API 主动推送给发送者 —— 这条一定能在会话里看到。
		target := strings.TrimSpace(msg.FromUser)
		text := reply
		go func() {
			defer recoverAndLog("wecom 主动回复")
			wc := NewWeCom(ns.toConfig())
			wc.SendTo(target, text)
		}()

		// ACK：返回空加密串，企微视为「已收到、无被动回复」，不会重试
		resp, err := crypt.encryptedResponse("", ts, nonce)
		if err != nil {
			logf("wecom 加密回复失败: %v", err)
			http.Error(w, "encrypt reply failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, resp)
	default:
		logf("wecom 方法不允许: %s", r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// dispatchWeComCommand 把微信文本消息路由成面板操作，返回要回复的文本。
func (s *Server) dispatchWeComCommand(content string) string {
	lower := strings.ToLower(content)
	switch {
	case lower == "" || lower == "帮助" || lower == "help":
		return "可用命令：\n· 状态 —— 查看面板运行状况\n· 重试失败 —— 重试所有失败或从未运行的目标\n· 重试 <站名> —— 重试指定站，例如「重试 星陨阁」\n· 帮助 —— 显示本信息"
	case lower == "状态" || lower == "status":
		return s.weComStatusText()
	case lower == "全部重试" || lower == "runall" || lower == "run all" || lower == "立即执行":
		if !s.automation.ExecuteAsync("微信指令：全部重试") {
			return "任务正在执行中，请稍后再试"
		}
		return "已提交：开始执行全部目标，完成后会自动推送通知"
	case lower == "失败列表" || lower == "failed" || lower == "failures":
		return s.weComFailedListText()
	case lower == "站点导入" || lower == "import":
		return s.weComImportSites()
	case lower == "站点名表同步" || lower == "syncsites":
		return s.weComSyncSiteTable()
	case strings.Contains(lower, "cookie"):
		return s.weComSyncCookies()
	case lower == "暂停定时" || lower == "pause":
		return s.weComSetSchedule(false)
	case lower == "恢复定时" || lower == "resume":
		return s.weComSetSchedule(true)
	case lower == "怎么重试单站" || lower == "重试指定站":
		return "直接发「重试 站名」即可，例如：\n· 重试 星陨阁\n· 重试 高清杜比\n发「状态」可先看失败的是哪些站"
	case lower == "重试失败" || lower == "retryfailed" || lower == "retry-failed" || lower == "retry all":
		sub := s.automation.ExecuteFailedAsync()
		switch sub.Result {
		case BatchAccepted:
			return fmt.Sprintf("已提交：正在重试 %d 个目标（失败或从未运行），结果稍后自动推送", len(sub.TargetURLs))
		case BatchBusy:
			return "任务正在执行中，请稍后再试"
		default:
			return "没有需要重试的目标（全部成功或未配置）"
		}
	case strings.HasPrefix(lower, "重试") || strings.HasPrefix(lower, "retry"):
		return s.weComRetryOne(content)
	default:
		return "未知命令。发「帮助」查看可用指令"
	}
}

// weComRetryOne 从消息里提取站名并匹配目标。
func (s *Server) weComRetryOne(content string) string {
	name := strings.TrimSpace(content)
	if i := strings.IndexAny(name, " \t"); i > 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	if name == "" {
		return "用法：重试 <站名>，例如「重试 星陨阁」"
	}
	targets := s.targets.Get()
	key := strings.ToLower(name)
	var matches []Target
	for _, t := range targets {
		if strings.Contains(strings.ToLower(t.DisplayName()), key) ||
			strings.Contains(strings.ToLower(t.URL), key) {
			matches = append(matches, t)
		}
	}
	if len(matches) == 0 {
		return fmt.Sprintf("没找到「%s」，请核对站名，或发「状态」看失败的是哪些", name)
	}
	if len(matches) > 1 {
		var names []string
		for _, t := range matches {
			names = append(names, t.DisplayName())
		}
		return fmt.Sprintf("「%s」匹配到多个：%s，请发更具体的名字", name, strings.Join(names, "、"))
	}
	switch s.automation.ExecuteTargetAsync(matches[0].URL) {
	case ExecAccepted:
		return fmt.Sprintf("已提交：正在重试「%s」，结果稍后自动推送", matches[0].DisplayName())
	case ExecBusy:
		return "任务正在执行中，请稍后"
	default:
		return "目标不存在"
	}
}

// weComStatusText 汇总面板当前状态。
func (s *Server) weComStatusText() string {
	running := s.automation.IsRunning()
	targets := s.targets.Get()
	latest := s.history.Latest()
	byURL := make(map[string]string, len(targets))
	for _, t := range targets {
		byURL[t.URL] = t.DisplayName()
	}
	success, failed := 0, 0
	var failedNames []string
	for url, rec := range latest {
		if rec.Success {
			success++
			continue
		}
		failed++
		if n := byURL[url]; n != "" {
			failedNames = append(failedNames, n)
		} else {
			failedNames = append(failedNames, url)
		}
	}
	var b strings.Builder
	b.WriteString("📊 PT 面板状态\n")
	if running {
		b.WriteString("⏳ 正在执行任务…\n")
	} else {
		b.WriteString("✅ 空闲\n")
	}
	b.WriteString(fmt.Sprintf("目标 %d 个｜最近一次：成功 %d / 失败 %d\n", len(targets), success, failed))
	if len(failedNames) > 0 {
		sort.Strings(failedNames)
		if len(failedNames) > 8 {
			b.WriteString(fmt.Sprintf("失败（前 8）：%s\n", strings.Join(failedNames[:8], "、")))
		} else {
			b.WriteString("失败：" + strings.Join(failedNames, "、"))
		}
	}
	return b.String()
}

// weComFailedListText 列出当前失败的目标及原因摘要。
func (s *Server) weComFailedListText() string {
	latest := s.history.Latest()
	targets := s.targets.Get()
	type row struct{ name, cat, detail string }
	var rows []row
	for _, t := range targets {
		r, ok := latest[t.URL]
		if !ok || r.Success {
			continue
		}
		cat, detail := classifyFailure(t, r.Message)
		rows = append(rows, row{t.DisplayName(), cat, detail})
	}
	if len(rows) == 0 {
		return "✅ 当前没有失败的目标"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "失败 %d 个：", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "\n· %s —— %s", r.name, r.cat)
	}
	b.WriteString("\n\n发「重试失败」可一键重试，或「重试 站名」单独重试")
	return b.String()
}

// weComSetSchedule 暂停/恢复定时任务。
func (s *Server) weComSetSchedule(enabled bool) string {
	cur := s.schedule.Current()
	if cur.Enabled == enabled {
		if enabled {
			return "定时任务本来就开着：" + cur.Describe
		}
		return "定时任务本来就是暂停状态"
	}
	cur.Enabled = enabled
	saved, err := s.schedule.Save(cur)
	if err != nil {
		return "保存失败：" + err.Error()
	}
	if s.scheduler != nil {
		if err := s.scheduler.Apply(saved); err != nil {
			return "应用失败：" + err.Error()
		}
	}
	if enabled {
		return "已恢复定时任务：" + saved.Describe
	}
	return "已暂停定时任务（手动指令仍可用）"
}

// weComImportSites 从 CookieCloud 识别 PT 站并补充到目标列表（与面板「导入站点」同逻辑）。
func (s *Server) weComImportSites() string {
	if s.cookies == nil || s.targets == nil {
		return "CookieCloud 未配置，无法导入"
	}
	data, lsData, err := s.cookies.FetchAll()
	if err != nil {
		return "拉取 CookieCloud 失败：" + err.Error()
	}
	res, err := RunAutoImport(data, lsData, s.targets)
	if err != nil {
		return "导入失败：" + err.Error()
	}
	if len(res.Added) == 0 {
		return fmt.Sprintf("没有新站点需要导入（已跳过 %d 个已存在的）", res.Skipped)
	}
	return fmt.Sprintf("✅ 已导入 %d 个新站点：%s", len(res.Added), strings.Join(res.Added, "、"))
}

// weComSyncCookies 刷新 CookieCloud 缓存，并反馈 PT 站的覆盖情况。
func (s *Server) weComSyncCookies() string {
	if s.cookies == nil {
		return "CookieCloud 未配置"
	}
	data, lsData, err := s.cookies.FetchAll()
	if err != nil {
		return "同步失败：" + err.Error()
	}
	syncCookiesOK(data)

	total := 0
	have := map[string]bool{}
	for domain, list := range data {
		total += len(list)
		d := normalizeDomain(domain)
		have[d] = true
		have[registeredDomain(d)] = true
	}
	// localStorage 也算凭据：馒头这类站登录态只在 localStorage，没有有效 cookie
	lsTotal := 0
	for domain, list := range lsData {
		lsTotal += len(list)
		d := normalizeDomain(domain)
		have[d] = true
		have[registeredDomain(d)] = true
	}
	ptSites := detectPTSitesWithStorage(data, lsData)

	targets := s.targets.Get()
	var missing []string
	for _, t := range targets {
		h := normalizeDomain(hostOf(t.URL))
		if h == "" {
			continue
		}
		if !have[h] && !have[registeredDomain(h)] {
			missing = append(missing, t.DisplayName())
		}
	}

	var b strings.Builder
	b.WriteString("✅ Cookie 已更新\n")
	fmt.Fprintf(&b, "识别 PT 站 %d 个（共 %d 个域名 / %d 条 Cookie", len(ptSites), len(data), total)
	if lsTotal > 0 {
		fmt.Fprintf(&b, "，另有 %d 个域名 / %d 条 localStorage", len(lsData), lsTotal)
	}
	b.WriteString("）\n")
	fmt.Fprintf(&b, "目标 %d 个，有凭据 %d 个", len(targets), len(targets)-len(missing))
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\n缺凭据 %d 个：%s", len(missing), strings.Join(missing, "、"))
	}
	return b.String()
}

// weComSyncSiteTable 从 MoviePilot 同步站点名表（与面板「站点名表」同逻辑）。
func (s *Server) weComSyncSiteTable() string {
	if s.mpSync == nil || s.ptSites == nil {
		return "MoviePilot 或站点表未初始化"
	}
	cfg := s.mpSync.Current()
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.Token) == "" {
		return "MoviePilot 未配置，请先在面板填地址和 Token"
	}
	n, err := SyncFromMoviePilot(cfg, s.ptSites)
	if err != nil {
		return "同步失败：" + err.Error()
	}
	return fmt.Sprintf("✅ 站点名表已同步：%d 个站点", n)
}
