package rabbitmq

import (
	"fmt"

	"github.com/go-logr/logr"
	amqp "github.com/rabbitmq/amqp091-go"
)

/*
 * mostly taken from https://github.com/dominodatalab/forge/blob/master/internal/message/amqp/publisher.go
 */

const (
	publishMandatory = true
	publishImmediate = false
)

type publisher struct {
	connection
	queueName string
}

func NewPublisher(uri, queueName string, logger logr.Logger) (*publisher, error) {
	c, err := NewConnection(uri, logger.WithName("Publisher"))
	if err != nil {
		c.log.Error(err, "error creating connection for publisher", "uri", c.uri)
		return nil, fmt.Errorf("error '%w' creating connection to '%v' for publisher", err, uri)
	}
	return &publisher{
		*c,
		queueName,
	}, nil
}

// In the event that the underlying connection was closed after connection creation, this function will attempt to
// reconnection to the AMQP broker before performing these operations.
func (p *publisher) Publish(payload SeldonPayloadWithHeaders) error {
	select {
	case <-p.err:
		p.log.Info("attempting to reconnect to rabbitmq", "uri", p.uri)

		if err := p.connect(); err != nil {
			p.log.Error(err, "error reconnecting to rabbitmq")
			return fmt.Errorf("error '%w' reconnecting to rabbitmq", err)
		}
	default:
	}

	body, err := payload.GetBytes()
	if err != nil {
		p.log.Error(err, "error retrieving payload bytes")
		return fmt.Errorf("error '%w' retrieving payload bytes", err)
	}
	message := amqp.Publishing{
		Headers:         StringMapToTable(payload.Headers),
		ContentType:     payload.GetContentType(),
		ContentEncoding: payload.GetContentEncoding(),
		DeliveryMode:    amqp.Persistent,
		Body:            body,
	}
	err = p.channel.Publish(amqpExchange, p.queueName, publishMandatory, publishImmediate, message)
	if err != nil {
		p.log.Error(err, "error publishing rabbitmq message")
		return fmt.Errorf("error '%w' publishing rabbitmq message", err)
	}
	return nil
}
