// Package feeds connects to Network Rail's open data services: the STOMP
// message broker for live feeds and the HTTPS endpoints for file downloads.
package feeds

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-stomp/stomp/v3"
)

// Config holds Network Rail open data credentials and endpoints.
type Config struct {
	Username string
	Password string
	// StompAddr is the broker host:port.
	StompAddr string
	// FilesURL is the base URL for timetable and reference downloads.
	FilesURL string
	// ClientID names durable subscriptions so messages queue on the broker
	// while trackside is briefly offline. It must be unique per instance.
	ClientID string
}

// Handler processes one message body. Returning an error is logged; the
// message is still acknowledged so a poison message cannot stall the feed.
type Handler func(ctx context.Context, body []byte) error

// ErrAuth means the feed rejected our credentials.
var ErrAuth = errors.New("credentials rejected")

// authFailuresBeforeStop is how many consecutive credential errors end a
// feed. Network Rail asks clients to stop, not keep retrying, on
// authentication errors; one retry allows for a broker hiccup.
const authFailuresBeforeStop = 2

// Subscribe consumes topics over a single connection, as Network Rail asks,
// until ctx is cancelled. It reconnects with exponential backoff whenever the
// connection drops, and gives up if the broker keeps rejecting the
// credentials.
func (c Config) Subscribe(ctx context.Context, topics map[string]Handler) {
	backoff := time.Second
	authFailures := 0
	for ctx.Err() == nil {
		start := time.Now()
		err := c.consume(ctx, topics)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrAuth) {
			authFailures++
			if authFailures >= authFailuresBeforeStop {
				slog.Error("Network Rail rejected the credentials; not retrying. Check NR_USERNAME and "+
					"NR_PASSWORD, that the account is active, and that it is subscribed to these feeds, "+
					"then restart.", "err", err)
				return
			}
		} else {
			authFailures = 0
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("feed connection lost; reconnecting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

func (c Config) consume(parent context.Context, topics map[string]Handler) error {
	conn, err := stomp.Dial("tcp", c.StompAddr,
		stomp.ConnOpt.Login(c.Username, c.Password),
		stomp.ConnOpt.HeartBeat(15*time.Second, 15*time.Second),
		stomp.ConnOpt.Header("client-id", c.ClientID),
	)
	if err != nil {
		if isAuthError(err) {
			return fmt.Errorf("dial: %w: %v", ErrAuth, err)
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Disconnect()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	errc := make(chan error, len(topics))
	for topic, h := range topics {
		sub, err := conn.Subscribe("/topic/"+topic, stomp.AckClientIndividual,
			stomp.SubscribeOpt.Header("activemq.subscriptionName", c.ClientID+"-"+topic))
		if err != nil {
			if isAuthError(err) {
				return fmt.Errorf("subscribe %s: %w: %v", topic, ErrAuth, err)
			}
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
		slog.Info("subscribed", "topic", topic)
		go func() { errc <- drain(ctx, conn, sub, topic, h) }()
	}
	select {
	case <-parent.Done():
		return nil
	case err := <-errc:
		return err
	}
}

// drain handles one subscription's messages in order.
func drain(ctx context.Context, conn *stomp.Conn, sub *stomp.Subscription, topic string, h Handler) error {
	for {
		select {
		case <-ctx.Done():
			sub.Unsubscribe()
			return nil
		case msg, ok := <-sub.C:
			if !ok {
				return fmt.Errorf("%s: subscription closed", topic)
			}
			if msg.Err != nil {
				if isAuthError(msg.Err) {
					return fmt.Errorf("%s: %w: %v", topic, ErrAuth, msg.Err)
				}
				return fmt.Errorf("%s: %w", topic, msg.Err)
			}
			if err := h(ctx, msg.Body); err != nil {
				slog.Warn("feed message failed", "topic", topic, "err", err)
			}
			if err := conn.Ack(msg); err != nil {
				return fmt.Errorf("%s: ack: %w", topic, err)
			}
		}
	}
}

// isAuthError recognises the broker's responses to bad or inactive
// credentials. ActiveMQ Artemis answers both with AMQ339009 "Exception
// getting session".
func isAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"amq339009", "security", "authenticat", "authoriz", "not allowed"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// Download fetches a gzip-compressed feed file and returns a reader over the
// decompressed content. The caller must close it.
func (c Config) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.FilesURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.Username, c.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s: %w (check NR_USERNAME/NR_PASSWORD, that the account "+
			"is active, and that you are subscribed to this feed)", path, resp.Status, ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s", path, resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %w", path, err)
	}
	return readCloser{gz, resp.Body}, nil
}

type readCloser struct {
	*gzip.Reader
	body io.Closer
}

func (r readCloser) Close() error {
	r.Reader.Close()
	return r.body.Close()
}

// Feed file paths relative to FilesURL.
const (
	CORPUSPath       = "/ntrod/SupportingFileAuthenticate?type=CORPUS"
	SMARTPath        = "/ntrod/SupportingFileAuthenticate?type=SMART"
	ScheduleFullPath = "/ntrod/CifFileAuthenticate?type=CIF_ALL_FULL_DAILY&day=toc-full"
)

// ScheduleUpdatePath is the daily update file named for weekday d. It is
// published on the morning of the following day.
func ScheduleUpdatePath(d time.Weekday) string {
	days := [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	return "/ntrod/CifFileAuthenticate?type=CIF_ALL_UPDATE_DAILY&day=toc-update-" + days[d]
}
