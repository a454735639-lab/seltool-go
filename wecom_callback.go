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
		http.Error(w, "callback disabled", http.StatusForbidden)
		return
	}
	crypt, err := NewWXBizMsgCrypt(strings.TrimSpace(ns.CallbackToken), strings.TrimSpace(ns.CallbackAESKey), strings.TrimSpace(ns.CorpID))
	if err != nil {
		http.Error(w, "bad callback config: "+err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	switch r.Method {
	case http.MethodGet:
		sig, ts, nonce := q.Get("msg_signature"), q.Get("timestamp"), q.Get("nonce")
		echostr := q.Get("echostr")
		if crypt.Sign(ts, nonce, echostr) != sig {
			http.Error(w, "bad signature", http.StatusForbidden)
			return
		}
		plain, err := crypt.Decrypt(echostr)
		if err != nil {
			http.Error(w, "decrypt failed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, plain)
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var req weComEncryptReq
		if err := xml.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad xml body", http.StatusBadRequest)
			return
		}
		ts, nonce, sig := q.Get("timestamp"), q.Get("nonce"), q.Get("msg_signature")
		if crypt.Sign(ts, nonce, req.Encrypt) != sig {
			http.Error(w, "bad signature", http.StatusForbidden)
			return
		}
		plainXML, err := crypt.Decrypt(req.Encrypt)
		if err != nil {
			http.Error(w, "decrypt failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		var msg weComTextMsg
		if err := xml.Unmarshal([]byte(plainXML), &msg); err != nil {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		if !containsWeComUser(ns.CallbackOwner, msg.FromUser) {
			http.Error(w, "forbidden: sender not in whitelist", http.StatusForbidden)
			return
		}
		reply := s.dispatchWeComCommand(strings.TrimSpace(msg.Content))
		resp, err := crypt.encryptedResponse(textReplyXML(msg.FromUser, msg.ToUser, reply), ts, nonce)
		if err != nil {
			http.Error(w, "encrypt reply failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, resp)
	default:
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
