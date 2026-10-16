// Package auth implements local account authentication with argon2id
// passwords and SQLite-backed sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// CookieName is the session cookie name shared with the frontend.
const CookieName = "pc_session"

// DefaultIdleTimeout is the maximum allowed session idle time.
const DefaultIdleTimeout = 12 * time.Hour

const (
	sessionTokenBytes = 32
	touchInterval     = time.Minute
	maxFailureDelay   = 2 * time.Second
	failureDelayStep  = 200 * time.Millisecond
	failureWindow     = 15 * time.Minute
	minPasswordLen    = 6
	maxPasswordLen    = 256
)

var (
	// ErrInvalidCredentials is returned for unknown users or wrong passwords.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidUsername is returned when an account name does not match policy.
	ErrInvalidUsername = errors.New("invalid username")
	// ErrWeakPassword is returned when a password is too short or too long.
	ErrWeakPassword = errors.New("invalid password")
	// ErrUserExists is returned when adding an account that already exists.
	ErrUserExists = errors.New("user already exists")
	// ErrOldPassword is returned when the current password does not match.
	ErrOldPassword = errors.New("old password mismatch")

	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

type failureState struct {
	count int
	last  time.Time
}

// Service authenticates users and manages sessions.
type Service struct {
	st   *store.Store
	idle time.Duration
	now  func() time.Time

	mu       sync.Mutex
	failures map[string]failureState
}

// NewService creates an authentication service backed by the SQLite store.
func NewService(st *store.Store) *Service {
	return &Service{st: st, idle: DefaultIdleTimeout, now: time.Now, failures: map[string]failureState{}}
}

// SetClock overrides the clock; used by tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// ValidateUsername checks the account naming policy.
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("%w: 只允许字母、数字与 . _ - 且以字母或数字开头，最长 32 字符", ErrInvalidUsername)
	}
	return nil
}

// ValidatePassword checks the password policy.
func ValidatePassword(password string) error {
	n := utf8.RuneCountInString(password)
	if n < minPasswordLen || n > maxPasswordLen {
		return fmt.Errorf("%w: 密码长度需在 %d-%d 个字符之间", ErrWeakPassword, minPasswordLen, maxPasswordLen)
	}
	return nil
}

// CreateUser adds a new account with the given password.
func (s *Service) CreateUser(username, password string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	if _, err := s.st.GetUser(username); err == nil {
		return fmt.Errorf("%w: %s", ErrUserExists, username)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.st.CreateUser(username, hash, s.now())
}

// SetPassword replaces the password of an existing account.
func (s *Service) SetPassword(username, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.st.UpdatePassword(username, hash, s.now()); err != nil {
		return err
	}
	// Existing sessions are invalidated because the credential changed.
	return s.st.DeleteUserSessions(username)
}

// DeleteUser removes an account and its sessions.
func (s *Service) DeleteUser(username string) error {
	return s.st.DeleteUser(username)
}

// ChangePassword verifies the current password before replacing it. The
// session identified by keepToken stays valid, every other session is dropped.
func (s *Service) ChangePassword(username, oldPassword, newPassword, keepToken string) error {
	u, err := s.st.GetUser(username)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(oldPassword, u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOldPassword
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.st.UpdatePassword(username, hash, s.now()); err != nil {
		return err
	}
	return s.st.DeleteUserSessionsExcept(username, hashToken(keepToken))
}

// Login verifies credentials, creates a session and returns its raw token.
// Repeated failures for the same account add an increasing delay.
func (s *Service) Login(username, password string) (string, error) {
	u, err := s.st.GetUser(username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Spend comparable time on unknown accounts so that response
			// timing does not reveal which names exist.
			_, _ = VerifyPassword(password, hashPlaceholder)
			s.penalize(username)
			return "", ErrInvalidCredentials
		}
		return "", err
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return "", err
	}
	if !ok {
		s.penalize(username)
		return "", ErrInvalidCredentials
	}

	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	sess := store.Session{
		TokenHash:  hashToken(token),
		Username:   username,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.idle),
	}
	if err := s.st.CreateSession(sess); err != nil {
		return "", err
	}
	if err := s.st.MarkLogin(username, now); err != nil {
		return "", err
	}
	s.clearFailures(username)
	return token, nil
}

// Authenticate resolves a session token to a username. Expired sessions are
// deleted and reported as unauthenticated.
func (s *Service) Authenticate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	key := hashToken(token)
	sess, err := s.st.GetSession(key)
	if err != nil {
		return "", false
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) {
		_ = s.st.DeleteSession(key)
		return "", false
	}
	if now.Sub(sess.LastSeenAt) >= touchInterval {
		_ = s.st.TouchSession(key, now, now.Add(s.idle))
	}
	return sess.Username, true
}

// Logout deletes the session behind the given token.
func (s *Service) Logout(token string) error {
	if token == "" {
		return nil
	}
	return s.st.DeleteSession(hashToken(token))
}

// Cookie builds the session cookie for a login response.
func (s *Service) Cookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.idle.Seconds()),
	}
}

// ExpiredCookie builds a cookie that deletes the current session on the client.
func (s *Service) ExpiredCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	}
}

// penalize records a failed attempt and sleeps for the current penalty delay.
func (s *Service) penalize(username string) {
	s.mu.Lock()
	st := s.failures[username]
	if s.now().Sub(st.last) > failureWindow {
		st.count = 0
	}
	st.count++
	st.last = s.now()
	s.failures[username] = st
	delay := time.Duration(st.count) * failureDelayStep
	s.mu.Unlock()

	if delay > maxFailureDelay {
		delay = maxFailureDelay
	}
	time.Sleep(delay)
}

func (s *Service) clearFailures(username string) {
	s.mu.Lock()
	delete(s.failures, username)
	s.mu.Unlock()
}

func newToken() (string, error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// hashPlaceholder is a valid argon2id hash with the parameters defined above,
// used to equalize the cost of unknown accounts and wrong passwords. It must be
// regenerated whenever the argon2 parameters change.
const hashPlaceholder = "$argon2id$v=19$m=19456,t=2,p=1$9FeQqjU4uc0jCoPp1ATWNA$PrwOgqcBUhAgSx8wbunG2zS9mOHl8yhOG1CUNWu8MQ4"
