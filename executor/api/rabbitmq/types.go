package rabbitmq

import (
	"github.com/seldonio/seldon-core/executor/api/payload"
	"github.com/wagslane/go-rabbitmq"
	"io"
)

// wrapper around `rabbitmq.Conn`
type ConnectionWrapper interface {
	io.Closer

	NewPublisher() (PublisherWrapper, error)
	NewConsumer(handler rabbitmq.Handler, queue string, consumerTag string) (ConsumerWrapper, error)
}

type ConsumerWrapper interface {
	Close()
}

type PublisherWrapper interface {
	Close()
	Publish(
		payload SeldonPayloadWithHeaders,
		queueName string,
	) error
}

type ConsumerError struct {
	err      error
	delivery rabbitmq.Delivery
	pl       *SeldonPayloadWithHeaders // might be nil
}

type SeldonPayloadWithHeaders struct {
	Payload payload.SeldonPayload
	Headers map[string][]string
}
