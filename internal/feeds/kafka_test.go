package feeds

import "testing"

// TestKafkaClientOptions catches invalid option combinations, which franz-go
// only reports when the client is built (for example setting both a dialer
// and a TLS config).
func TestKafkaClientOptions(t *testing.T) {
	c := KafkaConfig{Bootstrap: DefaultKafkaBootstrap, Username: "u", Password: "p", Group: "g", Topic: "t"}
	client, err := c.newClient()
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	client.Close()
}
