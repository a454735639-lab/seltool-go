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
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
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
	sess = &Session{Username: username, CSRF: randomToken(32)}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return id, sess
}

func (s *SessionStore) Get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}
