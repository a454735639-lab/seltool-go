package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{3,64}$`)

// errUserExists 用于区分「已注册」与「参数非法」两种失败。
var errUserExists = errors.New("用户已存在，注册入口已关闭")

type accountFile struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
}

// AccountStore 只维护一个本地管理员账户，且仅持久化 BCrypt 摘要。
type AccountStore struct {
	mu      sync.RWMutex
	file    string
	account *accountFile
}

func NewAccountStore(file string) *AccountStore {
	return &AccountStore{file: absPath(file)}
}

func (a *AccountStore) Load() error {
	data, err := os.ReadFile(a.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("无法读取用户账户文件: %w", err)
	}
	var acct accountFile
	if err := json.Unmarshal(data, &acct); err != nil {
		return fmt.Errorf("无法读取用户账户文件: %w", err)
	}
	if strings.TrimSpace(acct.Username) == "" || strings.TrimSpace(acct.PasswordHash) == "" {
		return fmt.Errorf("账户文件缺少用户名或密码摘要")
	}
	a.mu.Lock()
	a.account = &acct
	a.mu.Unlock()
	return nil
}

func (a *AccountStore) HasUser() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.account != nil
}

func (a *AccountStore) Register(username, password string) error {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("用户名需为 3-64 位字母、数字或 _ . @ -")
	}
	if len(password) < 8 || len(password) > 128 {
		return fmt.Errorf("密码长度需为 8-128 位")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.account != nil {
		return errUserExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("无法保存用户账户文件: %w", err)
	}
	replacement := accountFile{Username: username, PasswordHash: string(hash)}
	if err := writeJSONAtomic(a.file, replacement, 0o600); err != nil {
		return fmt.Errorf("无法保存用户账户文件: %w", err)
	}
	a.account = &replacement
	return nil
}

func (a *AccountStore) Authenticate(username, password string) bool {
	a.mu.RLock()
	acct := a.account
	a.mu.RUnlock()
	if acct == nil || acct.Username != username {
		// 仍然执行一次 bcrypt，避免用响应时间区分「用户不存在」与「密码错误」。
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(acct.PasswordHash), []byte(password)) == nil
}

func (a *AccountStore) Username() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.account == nil {
		return ""
	}
	return a.account.Username
}

var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// Session 保存登录态与 CSRF token。内存实现，重启即失效。
type Session struct {
	Username string
	CSRF     string
	// ExpiresAt 为服务端过期时间。此前会话永不过期：抓到/共享过 cookie 的人
	// 在进程重启前一直有效，且匿名请求每次都会新建一条永久记录。
	ExpiresAt time.Time
	// LastSeen 用于超出上限时淘汰最久未访问的会话。
	LastSeen time.Time
}

// 会话生命周期与总量上限。
const (
	sessionTTL     = 24 * time.Hour
	maxSessions    = 2048
	sessionSweepAt = 256 // 每新建这么多会话顺手清一次过期项

	// 匿名会话（只用于取 CSRF token，未登录）单独限额。
	// 之前匿名和已登录共用 2048 的池子，刷满即可把管理员挤下线。
	anonSessionTTL = 30 * time.Minute
	maxAnonSession = 256
)

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	created  int
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]*Session{}}
}

func randomToken(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func (s *SessionStore) Create(username string) (id string, sess *Session) {
	id = randomToken(32)
	now := time.Now()
	ttl := sessionTTL
	if username == "" {
		// 匿名会话只用来发 CSRF token，30 分钟足够，没必要占 24 小时。
		ttl = anonSessionTTL
	}
	sess = &Session{Username: username, CSRF: randomToken(32), ExpiresAt: now.Add(ttl), LastSeen: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	if s.created >= sessionSweepAt || len(s.sessions) >= maxSessions {
		s.created = 0
		s.evictLocked(now)
	}
	s.sessions[id] = sess
	s.trimAnonLocked(now)
	return id, sess
}

// evictLocked 清理过期会话；若仍超上限，淘汰最久未访问的，避免匿名请求把内存撑爆。
func (s *SessionStore) evictLocked(now time.Time) {
	for id, sess := range s.sessions {
		if !sess.ExpiresAt.IsZero() && now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
	for len(s.sessions) >= maxSessions {
		oldestID := ""
		var oldest time.Time
		// 先只找匿名会话：已登录会话不该被匿名流量挤掉。
		for id, sess := range s.sessions {
			if sess.Username != "" {
				continue
			}
			if oldestID == "" || sess.LastSeen.Before(oldest) {
				oldestID, oldest = id, sess.LastSeen
			}
		}
		if oldestID == "" {
			for id, sess := range s.sessions {
				if oldestID == "" || sess.LastSeen.Before(oldest) {
					oldestID, oldest = id, sess.LastSeen
				}
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.sessions, oldestID)
	}
}

// trimAnonLocked 把匿名会话数量压回上限，且只淘汰匿名会话。
func (s *SessionStore) trimAnonLocked(now time.Time) {
	anon := 0
	for _, sess := range s.sessions {
		if sess.Username == "" {
			anon++
		}
	}
	for anon > maxAnonSession {
		oldestID := ""
		var oldest time.Time
		for id, sess := range s.sessions {
			if sess.Username != "" {
				continue
			}
			if oldestID == "" || sess.LastSeen.Before(oldest) {
				oldestID, oldest = id, sess.LastSeen
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.sessions, oldestID)
		anon--
	}
}

func (s *SessionStore) Get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	now := time.Now()
	if !sess.ExpiresAt.IsZero() && now.After(sess.ExpiresAt) {
		s.Delete(id)
		return nil, false
	}
	s.mu.Lock()
	if cur, exists := s.sessions[id]; exists {
		cur.LastSeen = now
		sess = cur
	}
	s.mu.Unlock()
	return sess, true
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}
