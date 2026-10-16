package auth

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// newService creates an auth service on a temporary SQLite database, together
// with a controllable clock.
func newService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "console.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	clock := &fakeClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	svc := NewService(st)
	svc.SetClock(clock.Now)
	return svc, clock
}

// fakeClock is a controllable clock for session expiry tests.
type fakeClock struct{ now time.Time }

// Now returns the current fake time.
func (c *fakeClock) Now() time.Time { return c.now }

// Advance moves the clock forward.
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("admin123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword("admin123", hash)
	if err != nil || !ok {
		t.Fatalf("verify = %v (err %v), want true", ok, err)
	}
	if ok, _ := VerifyPassword("admin124", hash); ok {
		t.Errorf("wrong password verified")
	}
	if _, err := VerifyPassword("admin123", "$2y$notargon2"); !errors.Is(err, ErrInvalidHash) {
		t.Errorf("err = %v, want ErrInvalidHash", err)
	}
}

func TestHashPlaceholderUsesCurrentParameters(t *testing.T) {
	parts := strings.Split(hashPlaceholder, "$")
	if len(parts) != 6 {
		t.Fatalf("placeholder is not a valid encoded hash: %q", hashPlaceholder)
	}
	want := fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonTime, argonThreads)
	if parts[3] != want {
		t.Errorf("placeholder parameters = %q, want %q", parts[3], want)
	}
	if _, err := VerifyPassword("anything", hashPlaceholder); err != nil {
		t.Errorf("placeholder cannot be verified: %v", err)
	}
}

func TestCreateUserPolicy(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.CreateUser("admin", "admin123"); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate err = %v, want ErrUserExists", err)
	}
	if err := svc.CreateUser("bad name", "admin123"); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("username err = %v, want ErrInvalidUsername", err)
	}
	if err := svc.CreateUser("other", "123"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("password err = %v, want ErrWeakPassword", err)
	}
}

func TestLoginAndSessionLifecycle(t *testing.T) {
	svc, clock := newService(t)
	if err := svc.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Login("admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login("ghost", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user err = %v, want ErrInvalidCredentials", err)
	}

	token, err := svc.Login("admin", "admin123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if user, ok := svc.Authenticate(token); !ok || user != "admin" {
		t.Fatalf("authenticate = %q,%v, want admin,true", user, ok)
	}
	if _, ok := svc.Authenticate("not-a-token"); ok {
		t.Errorf("bogus token authenticated")
	}

	// The session stays valid while it is used, then expires when idle.
	clock.Advance(11 * time.Hour)
	if _, ok := svc.Authenticate(token); !ok {
		t.Fatalf("session expired too early")
	}
	clock.Advance(13 * time.Hour)
	if _, ok := svc.Authenticate(token); ok {
		t.Errorf("session should have expired after 12 hours of idle time")
	}
}

func TestLogoutAndPasswordChange(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := svc.Login("admin", "admin123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	second, err := svc.Login("admin", "admin123")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if err := svc.Logout(second); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, ok := svc.Authenticate(second); ok {
		t.Errorf("session still valid after logout")
	}

	if err := svc.ChangePassword("admin", "wrong", "newpass123", first); !errors.Is(err, ErrOldPassword) {
		t.Fatalf("err = %v, want ErrOldPassword", err)
	}
	if err := svc.ChangePassword("admin", "admin123", "newpass123", first); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, ok := svc.Authenticate(first); !ok {
		t.Errorf("current session was invalidated by the password change")
	}
	if _, err := svc.Login("admin", "admin123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works")
	}
	if _, err := svc.Login("admin", "newpass123"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestSetPasswordInvalidatesSessions(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("create: %v", err)
	}
	token, err := svc.Login("admin", "admin123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := svc.SetPassword("admin", "reset12345"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, ok := svc.Authenticate(token); ok {
		t.Errorf("session survived an administrative password reset")
	}
}

func TestCookie(t *testing.T) {
	svc, _ := newService(t)
	cookie := svc.Cookie("token", false)
	if cookie.Name != CookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("unexpected cookie: %+v", cookie)
	}
	if cookie.Secure {
		t.Errorf("cookie should not be secure by default")
	}
	if svc.Cookie("token", true).Secure != true {
		t.Errorf("secure flag was not propagated")
	}
	if svc.ExpiredCookie(false).MaxAge != -1 {
		t.Errorf("expired cookie should delete the session")
	}
}

func TestDeleteUserRemovesSessions(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("create: %v", err)
	}
	token, err := svc.Login("admin", "admin123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := svc.DeleteUser("admin"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := svc.Authenticate(token); ok {
		t.Errorf("session survived user deletion")
	}
	if _, err := svc.Login("admin", "admin123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("deleted user can still log in")
	}
}
