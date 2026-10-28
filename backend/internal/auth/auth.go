// Package auth implements login by one-time e-mail code and opaque sessions.
// Only hashes of codes and session tokens are stored.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxFailedAttempts burns a code after this many wrong guesses.
const MaxFailedAttempts = 5

var ErrInvalidCode = errors.New("invalid or expired code")
var ErrNoSession = errors.New("no session")

type Mailer interface {
	SendCode(ctx context.Context, email, code string) error
}

type Account struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	IsTeacher bool      `json:"isTeacher"`
	CreatedAt time.Time `json:"-"`
}

type Service struct {
	DB         *pgxpool.Pool
	Mailer     Mailer
	Now        func() time.Time
	CodeTTL    time.Duration
	SessionTTL time.Duration
	Pepper     string
}

func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func (s *Service) hash(parts ...string) []byte {
	h := sha256.Sum256([]byte(s.Pepper + "\x00" + strings.Join(parts, "\x00")))
	return h[:]
}

// RequestCode creates the account on first use and mails a fresh code.
func (s *Service) RequestCode(ctx context.Context, email string) error {
	email = NormalizeEmail(email)
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	var accountID string
	err = s.DB.QueryRow(ctx, `insert into accounts (email) values ($1)
		on conflict (email) do update set email = excluded.email returning id`, email).Scan(&accountID)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `insert into login_codes (account_id, code_hash, expires_at, created_at) values ($1, $2, $3, $4)`,
		accountID, s.hash("code", email, code), s.Now().Add(s.CodeTTL), s.Now())
	if err != nil {
		return err
	}
	return s.Mailer.SendCode(ctx, email, code)
}

// Verify exchanges the latest unused code for a session token.
func (s *Service) Verify(ctx context.Context, email, code string) (string, Account, error) {
	email = NormalizeEmail(email)
	now := s.Now()
	var codeID string
	var codeHash []byte
	var acc Account
	err := s.DB.QueryRow(ctx, `select c.id, c.code_hash, a.id, a.email, a.is_teacher
		from login_codes c join accounts a on a.id = c.account_id
		where a.email = $1 and c.used_at is null and c.expires_at > $2
		order by c.created_at desc limit 1`, email, now).Scan(&codeID, &codeHash, &acc.ID, &acc.Email, &acc.IsTeacher)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", Account{}, ErrInvalidCode
	}
	if err != nil {
		return "", Account{}, err
	}
	if subtle.ConstantTimeCompare(codeHash, s.hash("code", email, code)) != 1 {
		_, err := s.DB.Exec(ctx, `update login_codes set failed_attempts = failed_attempts + 1,
			used_at = case when failed_attempts + 1 >= $2 then $3::timestamptz else null end where id = $1`, codeID, MaxFailedAttempts, now)
		if err != nil {
			return "", Account{}, err
		}
		return "", Account{}, ErrInvalidCode
	}
	// The conditional update makes a concurrent second use of the same code fail.
	tag, err := s.DB.Exec(ctx, `update login_codes set used_at = $2 where id = $1 and used_at is null`, codeID, now)
	if err != nil {
		return "", Account{}, err
	}
	if tag.RowsAffected() != 1 {
		return "", Account{}, ErrInvalidCode
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Account{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	_, err = s.DB.Exec(ctx, `insert into sessions (token_hash, account_id, expires_at, created_at) values ($1, $2, $3, $4)`,
		s.hash("session", token), acc.ID, now.Add(s.SessionTTL), now)
	return token, acc, err
}

// Authenticate returns the account behind a live session token.
func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	var acc Account
	err := s.DB.QueryRow(ctx, `select a.id, a.email, a.is_teacher, a.created_at
		from sessions s join accounts a on a.id = s.account_id
		where s.token_hash = $1 and s.expires_at > $2`, s.hash("session", token), s.Now()).
		Scan(&acc.ID, &acc.Email, &acc.IsTeacher, &acc.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNoSession
	}
	return acc, err
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.DB.Exec(ctx, `delete from sessions where token_hash = $1`, s.hash("session", token))
	return err
}

// EnsureTeacher creates the account if needed and sets the teacher flag.
func (s *Service) EnsureTeacher(ctx context.Context, email string) error {
	_, err := s.DB.Exec(ctx, `insert into accounts (email, is_teacher) values ($1, true)
		on conflict (email) do update set is_teacher = true`, NormalizeEmail(email))
	return err
}
