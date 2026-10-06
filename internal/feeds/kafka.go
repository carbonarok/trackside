package feeds

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// DefaultKafkaBootstrap is the Rail Data Marketplace's Kafka cluster. Each
// product's Pub/Sub page shows the current value.
const DefaultKafkaBootstrap = "pkc-z3p1v0.europe-west2.gcp.confluent.cloud:9092"

// KafkaConfig is one Rail Data Marketplace product subscription. RDM issues
// a key, secret and consumer group per product; the group cannot be chosen.
type KafkaConfig struct {
	Bootstrap string
	Username  string
	Password  string
	Group     string
	Topic     string
}

// Enabled reports whether the subscription is configured.
func (c KafkaConfig) Enabled() bool {
	return c.Username != "" && c.Password != "" && c.Group != "" && c.Topic != ""
}

func isKafkaAuthError(err error) bool {
	return errors.Is(err, kerr.SaslAuthenticationFailed) ||
		errors.Is(err, kerr.GroupAuthorizationFailed) ||
		errors.Is(err, kerr.TopicAuthorizationFailed) ||
		errors.Is(err, kerr.ClusterAuthorizationFailed)
}

// Consume reads the topic until ctx is cancelled. Offsets are committed only
// after records are handled, so a restart resumes where it left off (within
// the broker's retention).
func (c KafkaConfig) Consume(ctx context.Context, h Handler) error {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(c.Bootstrap),
		kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}),
		kgo.Dialer((&net.Dialer{Timeout: 10 * time.Second}).DialContext),
		kgo.SASL(plain.Auth{User: c.Username, Pass: c.Password}.AsMechanism()),
		kgo.ConsumerGroup(c.Group),
		kgo.ConsumeTopics(c.Topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return fmt.Errorf("kafka client: %w", err)
	}
	defer client.Close()
	slog.Info("consuming", "topic", c.Topic)
	for {
		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if isKafkaAuthError(e.Err) {
					return fmt.Errorf("%w: %v", ErrAuth, e.Err)
				}
				slog.Warn("kafka fetch error", "topic", e.Topic, "partition", e.Partition, "err", e.Err)
			}
			// Errors such as bad credentials repeat immediately; back off.
			if fetches.NumRecords() == 0 {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(10 * time.Second):
				}
				continue
			}
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if err := h(ctx, r.Value); err != nil {
				slog.Warn("feed message failed", "topic", c.Topic, "err", err)
			}
		})
		if err := client.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("kafka commit failed", "topic", c.Topic, "err", err)
		}
	}
}
