package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// verifyJWT checks an ES256 provider token against the public key and
// returns its header and claims.
func verifyJWT(t *testing.T, tok string, pub *ecdsa.PublicKey) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %v, %d bytes", err, len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	var header, claims map[string]any
	h, _ := enc.DecodeString(parts[0])
	c, _ := enc.DecodeString(parts[1])
	json.Unmarshal(h, &header)
	json.Unmarshal(c, &claims)
	return header, claims
}

func TestProviderTokenIsAValidES256JWT(t *testing.T) {
	key, p8 := testKey(t)
	c, err := New(p8, "ABC123DEFG", "TEAM123456")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return at }
	tok, err := c.providerToken(false)
	if err != nil {
		t.Fatal(err)
	}
	header, claims := verifyJWT(t, tok, &key.PublicKey)
	if header["alg"] != "ES256" || header["kid"] != "ABC123DEFG" {
		t.Errorf("header %v", header)
	}
	if claims["iss"] != "TEAM123456" || claims["iat"] != float64(at.Unix()) {
		t.Errorf("claims %v", claims)
	}
	// Reused within its lifetime, replaced after.
	if again, _ := c.providerToken(false); again != tok {
		t.Error("token was not reused")
	}
	at = at.Add(tokenLifetime + time.Minute)
	if later, _ := c.providerToken(false); later == tok {
		t.Error("token was not refreshed")
	}
}

func TestSendSpeaksHTTP2WithLiveActivityHeaders(t *testing.T) {
	key, p8 := testKey(t)
	var calls atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.ProtoMajor != 2 {
			t.Errorf("protocol %s, want HTTP/2", r.Proto)
		}
		if r.URL.Path != "/3/device/abcdef0123" {
			t.Errorf("path %s", r.URL.Path)
		}
		for h, want := range map[string]string{
			"apns-topic":     "com.example.app.push-type.liveactivity",
			"apns-push-type": "liveactivity",
			"apns-priority":  "10",
		} {
			if got := r.Header.Get(h); got != want {
				t.Errorf("%s = %q, want %q", h, got, want)
			}
		}
		verifyJWT(t, strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), &key.PublicKey)
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"aps":{}}` {
			t.Errorf("body %s", body)
		}
		// The first attempt is told its provider token expired.
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"reason":"ExpiredProviderToken"}`)
			return
		}
		w.Header().Set("apns-id", "1234-5678")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	c, err := New(p8, "ABC123DEFG", "TEAM123456")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = srv.Client()
	resp, err := c.Send(context.Background(), srv.URL, Notification{
		DeviceToken: "abcdef0123",
		Topic:       "com.example.app.push-type.liveactivity",
		PushType:    "liveactivity",
		Priority:    10,
		Payload:     []byte(`{"aps":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK() || resp.ID != "1234-5678" {
		t.Errorf("response %+v, want accepted after a token refresh", resp)
	}
	if calls.Load() != 2 {
		t.Errorf("%d requests, want 2 (one retry with a fresh token)", calls.Load())
	}
}

func TestGoneAndBadTokenResponses(t *testing.T) {
	_, p8 := testKey(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			io.WriteString(w, `{"reason":"Unregistered","timestamp":1696680000000}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"reason":"BadDeviceToken"}`)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	c, _ := New(p8, "K", "T")
	c.HTTP = srv.Client()

	gone, err := c.Send(context.Background(), srv.URL, Notification{DeviceToken: "gone"})
	if err != nil || !gone.Gone() || gone.Status != http.StatusGone {
		t.Errorf("gone: %+v, %v", gone, err)
	}
	bad, err := c.Send(context.Background(), srv.URL, Notification{DeviceToken: "bad"})
	if err != nil || !bad.BadToken() || bad.Gone() {
		t.Errorf("bad: %+v, %v", bad, err)
	}
}

func TestParseKeyRejectsNonKeys(t *testing.T) {
	if _, err := ParseKey([]byte("not a key")); err == nil {
		t.Error("accepted text that isn't PEM")
	}
	if _, err := New([]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), "K", "T"); err == nil {
		t.Error("accepted a PEM block that isn't a key")
	}
}
