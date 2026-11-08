package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Lifetime is how long a session lasts. There is no "remember me".
const Lifetime = 12 * time.Hour

// CookieName is the name of the session cookie.
const CookieName = "session"

// ErrInvalid is returned by Open for a value that is malformed, tampered
// with, sealed under another key, or expired.
var ErrInvalid = errors.New("session: invalid or expired")

// Sessions seals credentials into cookies and opens them again.
type Sessions struct {
	aead cipher.AEAD
	path string
	now  func() time.Time
}

// payload is what gets sealed. Expiry lives inside it so a cookie kept past
// its Max-Age is still rejected.
type payload struct {
	User    string `json:"u"`
	Pass    string `json:"p"`
	Expires int64  `json:"e"`
}

// New returns Sessions keyed by the SHA-256 of secret, or by a random key
// when secret is empty. basePath is the cookie Path.
func New(secret, basePath string) (*Sessions, error) {
	var key []byte
	if secret == "" {
		key = make([]byte, 32)
		_, _ = rand.Read(key) // never fails; crashes the program instead
		slog.Info("REGISTRY_UI_SECRET is unset, using a random session key: everyone is logged out on restart")
	} else {
		sum := sha256.Sum256([]byte(secret))
		key = sum[:]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &Sessions{aead: aead, path: basePath, now: time.Now}, nil
}

// Seal encrypts and authenticates the credentials with their expiry.
func (s *Sessions) Seal(user, pass string, expires time.Time) string {
	b, _ := json.Marshal(payload{user, pass, expires.Unix()}) // strings and an int can't fail
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nil, nil, b, nil))
}

// Open reverses Seal. It returns ErrInvalid unless the value was sealed with
// this key, is untouched and hasn't expired.
func (s *Sessions) Open(v string) (user, pass string, err error) {
	sealed, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return "", "", ErrInvalid
	}
	b, err := s.aead.Open(nil, nil, sealed, nil)
	if err != nil {
		return "", "", ErrInvalid
	}
	var p payload
	if err := json.Unmarshal(b, &p); err != nil || !s.now().Before(time.Unix(p.Expires, 0)) {
		return "", "", ErrInvalid
	}
	return p.User, p.Pass, nil
}

// Set writes a session cookie holding the credentials, valid for Lifetime.
func (s *Sessions) Set(w http.ResponseWriter, r *http.Request, user, pass string) {
	c := s.cookie(r)
	c.Value = s.Seal(user, pass, s.now().Add(Lifetime))
	c.MaxAge = int(Lifetime / time.Second)
	http.SetCookie(w, c)
}

// Get returns the credentials from the request's session cookie. ok is false
// when there is no cookie or it doesn't open.
func (s *Sessions) Get(r *http.Request) (user, pass string, ok bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", "", false
	}
	user, pass, err = s.Open(c.Value)
	return user, pass, err == nil
}

// Clear deletes the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter, r *http.Request) {
	c := s.cookie(r)
	c.MaxAge = -1
	http.SetCookie(w, c)
}

func (s *Sessions) cookie(r *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Path:     s.path,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// A spoofed header only changes the sender's own cookie.
		Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	}
}
