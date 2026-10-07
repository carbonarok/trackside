// Package apns sends notifications through Apple's Push Notification
// service using token-based authentication: a .p8 signing key from the Apple
// Developer portal, its key ID and the team ID. It speaks HTTP/2 through the
// standard library, which negotiates it with APNs over TLS.
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Hosts for the two APNs environments. Apps built and run from Xcode get
// sandbox tokens; TestFlight and App Store builds get production ones.
const (
	Production = "https://api.push.apple.com"
	Sandbox    = "https://api.sandbox.push.apple.com"
)

// tokenLifetime is how long a provider token is reused. Apple rejects tokens
// older than an hour and refreshes more often than every 20 minutes.
const tokenLifetime = 40 * time.Minute

// Client signs and sends notifications. It is safe for concurrent use.
type Client struct {
	KeyID  string
	TeamID string
	HTTP   *http.Client
	// Now is the clock used for provider tokens; tests override it.
	Now func() time.Time

	key    *ecdsa.PrivateKey
	mu     sync.Mutex
	token  string
	issued time.Time
}

// New returns a client for the PEM-encoded .p8 key.
func New(keyPEM []byte, keyID, teamID string) (*Client, error) {
	key, err := ParseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	if keyID == "" || teamID == "" {
		return nil, errors.New("apns: key ID and team ID are required")
	}
	return &Client{
		KeyID:  keyID,
		TeamID: teamID,
		HTTP:   &http.Client{Timeout: 15 * time.Second},
		key:    key,
	}, nil
}

// ParseKey reads an Apple .p8 key: a PKCS #8 P-256 private key in PEM.
func ParseKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("apns: key is not PEM (expected the contents of the .p8 file)")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns: key is not an ECDSA key")
	}
	return ec, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// providerToken returns the current JWT, signing a new one when the old one
// has aged out or force is set.
func (c *Client) providerToken(force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !force && c.token != "" && now.Sub(c.issued) < tokenLifetime {
		return c.token, nil
	}
	tok, err := signJWT(c.key, c.KeyID, c.TeamID, now)
	if err != nil {
		return "", err
	}
	c.token, c.issued = tok, now
	return tok, nil
}

func signJWT(key *ecdsa.PrivateKey, keyID, teamID string, at time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	claims, _ := json.Marshal(map[string]any{"iss": teamID, "iat": at.Unix()})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign token: %w", err)
	}
	// JWS wants the raw 64-byte r || s, not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// Notification is one push.
type Notification struct {
	DeviceToken string // hex
	Topic       string // apns-topic
	PushType    string // apns-push-type
	Priority    int    // 10 immediate, 5 power-friendly
	Payload     []byte
}

// Response is APNs' answer. Status 200 means accepted; otherwise Reason is
// Apple's error code, such as "BadDeviceToken" or "Unregistered".
type Response struct {
	Status int
	Reason string
	ID     string
}

// OK reports whether APNs accepted the notification.
func (r Response) OK() bool { return r.Status == http.StatusOK }

// Gone reports that the token is no longer valid for the topic: for a Live
// Activity, that it has ended on the device.
func (r Response) Gone() bool {
	return r.Status == http.StatusGone || r.Reason == "Unregistered" || r.Reason == "ExpiredToken"
}

// BadToken reports that the token was not issued for this environment or
// is malformed.
func (r Response) BadToken() bool { return r.Reason == "BadDeviceToken" }

// Send delivers n to the APNs host (Production or Sandbox). A transport
// failure is an error; an APNs rejection is a Response.
func (c *Client) Send(ctx context.Context, host string, n Notification) (Response, error) {
	resp, err := c.send(ctx, host, n, false)
	if err == nil && (resp.Reason == "ExpiredProviderToken" || resp.Reason == "InvalidProviderToken") {
		resp, err = c.send(ctx, host, n, true)
	}
	return resp, err
}

func (c *Client) send(ctx context.Context, host string, n Notification, fresh bool) (Response, error) {
	tok, err := c.providerToken(fresh)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+n.DeviceToken, bytes.NewReader(n.Payload))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", n.Topic)
	req.Header.Set("apns-push-type", n.PushType)
	if n.Priority != 0 {
		req.Header.Set("apns-priority", strconv.Itoa(n.Priority))
	}
	req.Header.Set("content-type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("apns: %w", err)
	}
	defer res.Body.Close()
	out := Response{Status: res.StatusCode, ID: res.Header.Get("apns-id")}
	if res.StatusCode != http.StatusOK {
		var body struct {
			Reason string `json:"reason"`
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		json.Unmarshal(b, &body)
		out.Reason = body.Reason
	}
	return out, nil
}
