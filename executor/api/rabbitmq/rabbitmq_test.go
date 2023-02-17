package rabbitmq

import (
	"github.com/seldonio/seldon-core/executor/api/rest"
	"github.com/stretchr/testify/mock"
	"github.com/wagslane/go-rabbitmq"
)

type MockConnectionWrapper struct {
	mock.Mock
}

func (m *MockConnectionWrapper) Close() error {
	returnArgs := m.Called()
	return returnArgs.Error(0)
}

func (m *MockConnectionWrapper) NewPublisher() (PublisherWrapper, error) {
	returnArgs := m.Called()
	publisher := returnArgs.Get(0).(PublisherWrapper)
	return publisher, returnArgs.Error(1)
}

func (m *MockConnectionWrapper) NewConsumer(handler rabbitmq.Handler, queue string, consumerTag string) (ConsumerWrapper, error) {
	returnArgs := m.Called(handler, inputQueue, consumerTag)
	consumer := returnArgs.Get(0).(*TestConsumerWrapper) // unchecked for now
	consumer.handler = handler
	consumer.queue = queue
	consumer.consumerTag = consumerTag
	return consumer, returnArgs.Error(1)
}

type TestConsumerWrapper struct {
	handler     rabbitmq.Handler
	queue       string
	consumerTag string
	isClosed    bool
}

func (m *TestConsumerWrapper) Close() {
	println("closed consumer wrapper")
	m.isClosed = true
}

func (m *TestConsumerWrapper) SimulateDelivery(delivery rabbitmq.Delivery) rabbitmq.Action {
	return m.handler(delivery)
}

type MockPublisherWrapper struct {
	mock.Mock
}

func (m *MockPublisherWrapper) Close() {
	m.Called()
}

func (m *MockPublisherWrapper) Publish(
	data []byte,
	routingKeys []string,
	optionFuncs ...func(*rabbitmq.PublishOptions),
) error {
	returnArgs := m.Called(data, routingKeys, optionFuncs)
	return returnArgs.Error(0)
}

type TestPayload struct {
	Msg string
}

func (s *TestPayload) GetPayload() interface{} {
	return s.Msg
}

func (s *TestPayload) GetContentType() string {
	return rest.ContentTypeJSON
}

func (s *TestPayload) GetContentEncoding() string {
	return ""
}

func (s *TestPayload) GetBytes() ([]byte, error) {
	return []byte(s.Msg), nil
}
