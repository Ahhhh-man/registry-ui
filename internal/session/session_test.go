package session

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	user = "alice"
	pass = "hunter2-s3cret-pass"
)

func newSessions(t *testing.T, secret string) *Sessions {
	t.Helper()
	s, err := New(secret, "/registry/")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := newSessions(t, "secret")
	v := s.Seal(user, pass, time.Now().Add(time.Hour))
	if strings.Contains(v, pass) || strings.Contains(v, user) {
		t.Fatalf("sealed value %q leaks the credentials", v)
	}
	u, p, err := s.Open(v)
	if err != nil || u != user || p != pass {
		t.Fatalf("Open = %q, %q, %v; want %q, %q, nil", u, p, err, user, pass)
	}
	// Same secret, new process: still opens.
	if _, _, err := newSessions(t, "secret").Open(v); err != nil {
		t.Fatalf("same secret, new instance: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	s := newSessions(t, "secret")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	rec := httptest.NewRecorder()
	s.Set(rec, httptest.NewRequest("GET", "/", nil), user, pass)
	v := rec.Result().Cookies()[0].Value

	now = now.Add(Lifetime - time.Second)
	if _, _, err := s.Open(v); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	now = now.Add(time.Second)
	if _, _, err := s.Open(v); err != ErrInvalid {
		t.Fatalf("at expiry: err = %v, want ErrInvalid", err)
	}
}

func TestTamperedByte(t *testing.T) {
	s := newSessions(t, "secret")
	raw, _ := base64.RawURLEncoding.DecodeString(s.Seal(user, pass, time.Now().Add(time.Hour)))
	for i := range raw {
		b := append([]byte(nil), raw...)
		b[i] ^= 1
		if _, _, err := s.Open(base64.RawURLEncoding.EncodeToString(b)); err != ErrInvalid {
			t.Fatalf("flipped byte %d: err = %v, want ErrInvalid", i, err)
		}
	}
	for _, v := range []string{"", "not base64!", "c2hvcnQ"} {
		if _, _, err := s.Open(v); err != ErrInvalid {
			t.Errorf("Open(%q): err = %v, want ErrInvalid", v, err)
		}
	}
}

func TestWrongKey(t *testing.T) {
	v := newSessions(t, "secret").Seal(user, pass, time.Now().Add(time.Hour))
	if _, _, err := newSessions(t, "other").Open(v); err != ErrInvalid {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestRandomKeyRestart(t *testing.T) {
	before := newSessions(t, "")
	v := before.Seal(user, pass, time.Now().Add(time.Hour))
	if _, _, err := before.Open(v); err != nil {
		t.Fatalf("same process: %v", err)
	}
	if _, _, err := newSessions(t, "").Open(v); err != ErrInvalid {
		t.Fatalf("after restart: err = %v, want ErrInvalid", err)
	}
}

func TestCookie(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		proto  string
		secure bool
	}{
		{"plain http", "http://ui/", "", false},
		{"tls", "https://ui/", "", true},
		{"forwarded https", "http://ui/", "https", true},
		{"forwarded http", "http://ui/", "http", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSessions(t, "secret")
			r := httptest.NewRequest("GET", tt.url, nil)
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}

			rec := httptest.NewRecorder()
			s.Set(rec, r, user, pass)
			c := rec.Result().Cookies()[0]
			if c.Name != CookieName || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode ||
				c.Path != "/registry/" || c.MaxAge != 12*60*60 || c.Secure != tt.secure {
				t.Fatalf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/registry/, Max-Age=43200, Secure=%v", c, tt.secure)
			}
			if strings.Contains(rec.Header().Get("Set-Cookie"), pass) {
				t.Fatal("Set-Cookie leaks the password")
			}

			r.AddCookie(c)
			if u, p, ok := s.Get(r); !ok || u != user || p != pass {
				t.Fatalf("Get = %q, %q, %v", u, p, ok)
			}

			rec = httptest.NewRecorder()
			s.Clear(rec, r)
			c = rec.Result().Cookies()[0]
			if c.Name != CookieName || c.MaxAge != -1 || c.Value != "" || c.Path != "/registry/" ||
				!c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure != tt.secure {
				t.Fatalf("cleared cookie = %+v", c)
			}
		})
	}
}

func TestGetWithoutCookie(t *testing.T) {
	s := newSessions(t, "secret")
	r := httptest.NewRequest("GET", "/", nil)
	if _, _, ok := s.Get(r); ok {
		t.Fatal("ok with no cookie")
	}
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "garbage"})
	if _, _, ok := s.Get(r); ok {
		t.Fatal("ok with a garbage cookie")
	}
}
