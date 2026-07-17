package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	DefaultSubject = "kernel.events"
	DefaultStream  = "PLATFORM_OPERATIONS"
)

type Config struct {
	URL             string
	ClientName      string
	Token           string
	CredentialsFile string
	RootCAFile      string
	ClientCertFile  string
	ClientKeyFile   string
	Subject         string
	Stream          string
	ConnectTimeout  time.Duration
	PublishTimeout  time.Duration
}

type jetStreamPublisher interface {
	Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

type Publisher struct {
	js             jetStreamPublisher
	subject        string
	stream         string
	publishTimeout time.Duration
}

// Connect establishes a production JetStream connection. Stream creation is
// intentionally kept in the admin bootstrap script; applications can only
// publish to the expected pre-provisioned stream.
func Connect(config Config) (*Publisher, *nats.Conn, error) {
	if strings.TrimSpace(config.URL) == "" {
		return nil, nil, errors.New("NATS_URL is required")
	}
	if (config.ClientCertFile == "") != (config.ClientKeyFile == "") {
		return nil, nil, errors.New("NATS client certificate and key must be configured together")
	}

	name := config.ClientName
	if name == "" {
		name = "kernel-api"
	}
	timeout := config.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	options := []nats.Option{
		nats.Name(name),
		nats.Timeout(timeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	}
	if config.Token != "" {
		options = append(options, nats.Token(config.Token))
	}
	if config.CredentialsFile != "" {
		options = append(options, nats.UserCredentials(config.CredentialsFile))
	}
	if config.RootCAFile != "" {
		options = append(options, nats.RootCAs(config.RootCAFile))
	}
	if config.ClientCertFile != "" {
		options = append(options, nats.ClientCert(config.ClientCertFile, config.ClientKeyFile))
	}

	connection, err := nats.Connect(config.URL, options...)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to NATS: %w", err)
	}
	js, err := jetstream.New(connection)
	if err != nil {
		connection.Close()
		return nil, nil, fmt.Errorf("initialize JetStream: %w", err)
	}
	return NewPublisher(js, config), connection, nil
}

func NewPublisher(js jetStreamPublisher, config Config) *Publisher {
	subject := config.Subject
	if subject == "" {
		subject = DefaultSubject
	}
	stream := config.Stream
	if stream == "" {
		stream = DefaultStream
	}
	publishTimeout := config.PublishTimeout
	if publishTimeout <= 0 {
		publishTimeout = 10 * time.Second
	}
	return &Publisher{js: js, subject: subject, stream: stream, publishTimeout: publishTimeout}
}

func (p *Publisher) Publish(ctx context.Context, event kernelv1.DomainEventEnvelope[json.RawMessage]) error {
	if p == nil || p.js == nil {
		return errors.New("JetStream publisher is not configured")
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate event: %w", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	publishCtx, cancel := context.WithTimeout(ctx, p.publishTimeout)
	defer cancel()
	ack, err := p.js.Publish(
		publishCtx,
		p.subject,
		payload,
		jetstream.WithMsgID(event.EventID),
		jetstream.WithExpectStream(p.stream),
	)
	if err != nil {
		return fmt.Errorf("publish event %s: %w", event.EventID, err)
	}
	if ack == nil || ack.Stream != p.stream {
		return fmt.Errorf("publish event %s: unexpected JetStream acknowledgement", event.EventID)
	}
	return nil
}
