package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "ly_route_session"

type AuthConfig struct {
	AdminUsername    string
	AdminPassword    string
	ReadonlyUsername string
	ReadonlyPassword string
	CookieSecure     bool
	ForcePasswordChange bool
}

func (config AuthConfig) configured() bool {
	return strings.TrimSpace(config.AdminUsername) != "" && config.AdminPassword != ""
}

func (config AuthConfig) readonlyConfigured() bool {
	return strings.TrimSpace(config.ReadonlyUsername) != "" && config.ReadonlyPassword != ""
}

type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}

type AuditEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`
	Role      string    `json:"role"`
	Resource  string    `json:"resource"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	RequestID string    `json:"request_id"`
}

type Session struct {
	ID                     string    `json:"id"`
	Username               string    `json:"username"`
	Role                   string    `json:"role"`
	CreatedAt              time.Time `json:"created_at"`
	PasswordChangeRequired bool      `json:"password_change_required,omitempty"`
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	now      func() time.Time
}

func newSessionStore(now func() time.Time) *sessionStore {
	if now == nil {
		now = time.Now
	}
	return &sessionStore{sessions: map[string]Session{}, now: now}
}

func (store *sessionStore) create(username, role string, passwordChangeRequired bool) Session {
	store.mu.Lock()
	defer store.mu.Unlock()
	session := Session{ID: randomToken(), Username: username, Role: role, CreatedAt: store.now().UTC(), PasswordChangeRequired: passwordChangeRequired}
	store.sessions[session.ID] = session
	return session
}

func (store *sessionStore) get(id string) (Session, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.sessions[id]
	return session, ok
}

func (store *sessionStore) delete(id string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.sessions, id)
}

func defaultAuthConfig() AuthConfig {
	return AuthConfig{}
}

func constantEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func hashPassword(username, password string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(username) + "\x00" + password))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateNewPassword(username, password string) error {
	if len(password) < 8 {
		return fmt.Errorf("new password must be at least 8 characters")
	}
	if strings.Contains(strings.ToLower(password), strings.ToLower(strings.TrimSpace(username))) {
		return fmt.Errorf("new password must not contain the username")
	}
	return nil
}

func randomToken() string {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}

func sessionCookie(id string, secure bool) *http.Cookie {
	return &http.Cookie{Name: sessionCookieName, Value: id, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode}
}

func clearSessionCookie(secure bool) *http.Cookie {
	cookie := sessionCookie("", secure)
	cookie.MaxAge = -1
	return cookie
}

func bearerToken(header string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func auditSafeText(value string) string {
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"xray://", "vmess://", "vless://", "trojan://", "ss://", "subscription", "credential", "password", "token", "secret", "private_key"} {
		if strings.Contains(lower, forbidden) {
			return "redacted audit detail"
		}
	}
	return value
}
