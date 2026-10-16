// Package store persists console state (accounts and sessions) in SQLite.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

//go:embed migrations/*.sql
var migrations embed.FS

// Store wraps the console SQLite database.
type Store struct {
	db *sql.DB
}

// User is an account record.
type User struct {
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// Session is a persisted login session.
type Session struct {
	TokenHash  string
	Username   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Open opens (and migrates) the SQLite database at path.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection avoids SQLITE_BUSY under concurrent writes; console
	// traffic is low volume.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const timeLayout = time.RFC3339Nano

// CreateUser inserts a new account.
func (s *Store) CreateUser(username, passwordHash string, now time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, now.UTC().Format(timeLayout), now.UTC().Format(timeLayout))
	if err != nil {
		return fmt.Errorf("insert user %s: %w", username, err)
	}
	return nil
}

// GetUser returns one account by name.
func (s *Store) GetUser(username string) (User, error) {
	row := s.db.QueryRow(
		`SELECT username, password_hash, created_at, updated_at, last_login_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

// ListUsers returns all account names in alphabetical order.
func (s *Store) ListUsers() ([]string, error) {
	rows, err := s.db.Query(`SELECT username FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// UpdatePassword replaces the password hash of an account.
func (s *Store) UpdatePassword(username, passwordHash string, now time.Time) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE username = ?`,
		passwordHash, now.UTC().Format(timeLayout), username)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return requireAffected(res, username)
}

// MarkLogin records the last successful login time of an account.
func (s *Store) MarkLogin(username string, now time.Time) error {
	res, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE username = ?`,
		now.UTC().Format(timeLayout), username)
	if err != nil {
		return fmt.Errorf("mark login: %w", err)
	}
	return requireAffected(res, username)
}

// DeleteUser removes an account and its sessions.
func (s *Store) DeleteUser(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return requireAffected(res, username)
}

// CreateSession stores a new session record.
func (s *Store) CreateSession(sess Session) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, username, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.Username, sess.CreatedAt.UTC().Format(timeLayout),
		sess.LastSeenAt.UTC().Format(timeLayout), sess.ExpiresAt.UTC().Format(timeLayout))
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// GetSession returns one session by token hash.
func (s *Store) GetSession(tokenHash string) (Session, error) {
	row := s.db.QueryRow(
		`SELECT token_hash, username, created_at, last_seen_at, expires_at FROM sessions WHERE token_hash = ?`, tokenHash)
	var sess Session
	var created, seen, expires string
	if err := row.Scan(&sess.TokenHash, &sess.Username, &created, &seen, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("scan session: %w", err)
	}
	var err error
	if sess.CreatedAt, err = time.Parse(timeLayout, created); err != nil {
		return Session{}, fmt.Errorf("parse session created_at: %w", err)
	}
	if sess.LastSeenAt, err = time.Parse(timeLayout, seen); err != nil {
		return Session{}, fmt.Errorf("parse session last_seen_at: %w", err)
	}
	if sess.ExpiresAt, err = time.Parse(timeLayout, expires); err != nil {
		return Session{}, fmt.Errorf("parse session expires_at: %w", err)
	}
	return sess, nil
}

// TouchSession refreshes the last-seen timestamp of a session.
func (s *Store) TouchSession(tokenHash string, seen, expires time.Time) error {
	res, err := s.db.Exec(`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`,
		seen.UTC().Format(timeLayout), expires.UTC().Format(timeLayout), tokenHash)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return requireAffected(res, tokenHash)
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUserSessionsExcept removes the sessions of an account except one.
func (s *Store) DeleteUserSessionsExcept(username, keepTokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE username = ? AND token_hash <> ?`, username, keepTokenHash)
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// DeleteUserSessions removes every session of an account.
func (s *Store) DeleteUserSessions(username string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// DeleteExpiredSessions prunes sessions that expired before now.
func (s *Store) DeleteExpiredSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now.UTC().Format(timeLayout))
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

func requireAffected(res sql.Result, name string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return nil
}

func scanUser(row *sql.Row) (User, error) {
	var u User
	var created, updated string
	var lastLogin sql.NullString
	if err := row.Scan(&u.Username, &u.PasswordHash, &created, &updated, &lastLogin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	var err error
	if u.CreatedAt, err = time.Parse(timeLayout, created); err != nil {
		return User{}, fmt.Errorf("parse user created_at: %w", err)
	}
	if u.UpdatedAt, err = time.Parse(timeLayout, updated); err != nil {
		return User{}, fmt.Errorf("parse user updated_at: %w", err)
	}
	if lastLogin.Valid && lastLogin.String != "" {
		t, err := time.Parse(timeLayout, lastLogin.String)
		if err != nil {
			return User{}, fmt.Errorf("parse user last_login_at: %w", err)
		}
		u.LastLoginAt = &t
	}
	return u, nil
}
