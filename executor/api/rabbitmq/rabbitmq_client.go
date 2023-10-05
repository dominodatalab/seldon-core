package rabbitmq

import (
	"fmt"
	"github.com/go-logr/logr"
	"github.com/wagslane/go-rabbitmq"
)

const (
	publishMandatory = true
	publishImmediate = false
)

type RabbitMqConnection struct {
	conn *rabbitmq.Conn
	log  logr.Logger
}

type RabbitMqConsumer struct {
	rabbitmq.Consumer
}

type RabbitMqPublisher struct {
	rabbitmq.Publisher
	log logr.Logger
}

func createRabbitMQConnection(brokerUrl string, log logr.Logger) (ConnectionWrapper, error) {
	conn, err := rabbitmq.NewConn(brokerUrl, rabbitmq.WithConnectionOptionsLogging)
	if err != nil {
		log.Error(err, "error connecting to rabbitmq")
		return nil, fmt.Errorf("error '%w' connecting to rabbitmq", err)
	}
	return &RabbitMqConnection{conn, log}, nil
}

func (c *RabbitMqConnection) Close() error {
	return c.conn.Close()
}

func (c *RabbitMqConnection) NewPublisher() (PublisherWrapper, error) {
	publisher, err := rabbitmq.NewPublisher(
		c.conn,
		rabbitmq.WithPublisherOptionsLogging,
		rabbitmq.WithPublisherOptionsExchangeName(""), //default exchange
	)
	if err != nil {
		c.log.Error(err, "error creating publisher")
		return nil, fmt.Errorf("error '%w' creating publisher", err)
	}
	return &RabbitMqPublisher{*publisher, c.log}, nil
}

func (c *RabbitMqConnection) NewConsumer(handler rabbitmq.Handler, queue string, consumerTag string) (ConsumerWrapper, error) {
	consumer, err := rabbitmq.NewConsumer(
		c.conn,
		handler,
		queue,
		rabbitmq.WithConsumerOptionsConsumerName(consumerTag),
		rabbitmq.WithConsumerOptionsQueueNoDeclare,
	)
	if err != nil {
		c.log.Error(err, "error creating publisher")
		return nil, fmt.Errorf("error '%w' creating publisher", err)
	}
	return &RabbitMqConsumer{*consumer}, nil
}

func (c *RabbitMqConsumer) Close() {
	c.Consumer.Close()
}

func (p *RabbitMqPublisher) Close() {
	p.Publisher.Close()
}

func (p *RabbitMqPublisher) Publish(
	payloadWithHeaders SeldonPayloadWithHeaders,
	queueName string,
) error {

	payload := payloadWithHeaders.Payload
	body, err := payload.GetBytes()
	if err != nil {
		p.log.Error(err, "error retrieving payload bytes")
		return fmt.Errorf("error '%w' retrieving payload bytes", err)
	}

	options := []func(options *rabbitmq.PublishOptions){
		rabbitmq.WithPublishOptionsHeaders(StringMapToTable(payloadWithHeaders.Headers)),
		rabbitmq.WithPublishOptionsContentType(payload.GetContentType()),
		rabbitmq.WithPublishOptionsContentEncoding(""),
		rabbitmq.WithPublishOptionsPersistentDelivery,
	}
	if publishMandatory {
		options = append(options, rabbitmq.WithPublishOptionsMandatory)
	}
	if publishImmediate {
		options = append(options, rabbitmq.WithPublishOptionsImmediate)
	}

	err = p.Publisher.Publish(
		body,
		[]string{queueName},
		options...,
	)
	if err != nil {
		p.log.Error(err, "error publishing rabbitmq message")
		return fmt.Errorf("error '%w' publishing rabbitmq message", err)
	}

	return nil
}

func CreateConsumerHandler(
	payloadHandler func(*SeldonPayloadWithHeaders) error,
	errorHandler func(args ConsumerError) error,
	log logr.Logger,
) rabbitmq.Handler {
	return func(delivery rabbitmq.Delivery) rabbitmq.Action {
		pl, err := DeliveryToPayload(delivery)
		if err != nil {
			return handleConsumerError(ConsumerError{err, delivery, pl}, errorHandler, log)
		}
		err = payloadHandler(pl)
		if err != nil {
			return handleConsumerError(ConsumerError{err, delivery, pl}, errorHandler, log)
		}
		return rabbitmq.Ack
	}
}

func handleConsumerError(
	args ConsumerError,
	errorHandler func(args ConsumerError) error,
	log logr.Logger,
) rabbitmq.Action {

	err := errorHandler(args)
	if err != nil {
		log.Error(err, "error handler encountered an error", "original error", args.err)
	}

	return rabbitmq.NackDiscard
}
