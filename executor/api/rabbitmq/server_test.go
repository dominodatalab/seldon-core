package rabbitmq

import (
	"bytes"
	"fmt"
	"github.com/go-logr/logr/testr"
	"github.com/golang/protobuf/jsonpb"
	guuid "github.com/google/uuid"
	. "github.com/onsi/gomega"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/seldonio/seldon-core/executor/api"
	"github.com/seldonio/seldon-core/executor/api/grpc/seldon/proto"
	"github.com/seldonio/seldon-core/executor/api/payload"
	"github.com/seldonio/seldon-core/executor/api/rest"
	"github.com/seldonio/seldon-core/executor/api/test"
	v1 "github.com/seldonio/seldon-core/operator/apis/machinelearning.seldon.io/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wagslane/go-rabbitmq"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var (
	model           = v1.MODEL
	deploymentName  = "testDep"
	namespace       = "testNs"
	protocol        = api.ProtocolSeldon
	transport       = api.TransportRest
	brokerUrl       = "amqp://something.com"
	inputQueue      = "inputQueue"
	outputQueue     = "outputQueue"
	fullHealthCheck = false
)

func TestRabbitMqServer(t *testing.T) {
	log := testr.New(t)
	// this bit down to where `p` is defined is taken from rest/server_test.go
	g := NewGomegaWithT(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := ioutil.ReadAll(r.Body)
		g.Expect(err).To(BeNil())
		g.Expect(r.Header.Get(payload.SeldonPUIDHeader)).To(Equal("1"))
		//called = true
		_, _ = w.Write(bodyBytes)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	serverUrl, setupErr := url.Parse(server.URL)
	g.Expect(setupErr).Should(BeNil())
	urlParts := strings.Split(serverUrl.Host, ":")
	port, setupErr := strconv.Atoi(urlParts[1])
	g.Expect(setupErr).Should(BeNil())

	p := v1.PredictorSpec{
		Name: "p",
		Graph: v1.PredictiveUnit{
			Type: &model,
			Endpoint: &v1.Endpoint{
				ServiceHost: urlParts[0],
				ServicePort: int32(port),
				Type:        v1.REST,
				HttpPort:    int32(port),
			},
		},
	}
	serverUrl, _ = url.Parse("http://localhost")

	testServer := SeldonRabbitMQServer{
		Client:          test.SeldonMessageTestClient{},
		DeploymentName:  deploymentName,
		Namespace:       namespace,
		Protocol:        protocol,
		Transport:       transport,
		ServerUrl:       *serverUrl,
		Predictor:       p,
		BrokerUrl:       brokerUrl,
		InputQueueName:  inputQueue,
		OutputQueueName: outputQueue,
		Log:             log,
		FullHealthCheck: fullHealthCheck,
	}

	testPuid := guuid.New().String()
	testHeaders := map[string]interface{}{payload.SeldonPUIDHeader: testPuid}

	// test predictor process returns the request payload as the response
	validJson := fmt.Sprintf(
		`{"meta":{"puid":"%v"},"jsonData":{"start":1,"stop":10}}`,
		testPuid,
	)
	validRequestDelivery := rabbitmq.Delivery{
		Delivery: amqp.Delivery{
			ContentType:  "application/json",
			DeliveryMode: rabbitmq.Persistent,
			Body:         []byte(validJson),
			Headers:      testHeaders,
		},
	}
	validPayloadResponseWithHeaders := SeldonPayloadWithHeaders{
		&payload.BytesPayload{
			Msg:         []byte(validJson),
			ContentType: "application/json",
		},
		TableToStringMap(testHeaders),
	}

	// test predictor process returns the request payload as the response
	validJsonNoPuid := `{"jsonData":{"start":1,"stop":10}}`
	validRequestNoPuidDelivery := rabbitmq.Delivery{
		Delivery: amqp.Delivery{
			ContentType:  "application/json",
			DeliveryMode: rabbitmq.Persistent,
			Body:         []byte(validJsonNoPuid),
			Headers:      map[string]interface{}{},
		},
	}
	validPayloadResponseNoPuid :=
		&payload.BytesPayload{
			Msg:         []byte(validJsonNoPuid),
			ContentType: "application/json",
		}

	invalidContentType := "bogus"
	invalidDelivery := rabbitmq.Delivery{
		Delivery: amqp.Delivery{
			ContentType: invalidContentType,
			Body:        []byte(`bogus`),
			Headers:     testHeaders,
		},
	}
	invalidJsonResponse := fmt.Sprintf(
		`{"status":{"info":"Prediction Failed","reason":"unknown payload type '%v'","status":"FAILURE"}}`,
		invalidContentType,
	)
	invalidPayloadResponse :=
		&payload.BytesPayload{
			Msg:         []byte(invalidJsonResponse),
			ContentType: "application/json",
		}

	// test predictor process returns the request payload as the response
	errorJson := fmt.Sprintf(
		`{"status":{"info":"Prediction Failed","reason":"unknown payload type 'bogus'","status":"FAILURE"},"meta":{"puid":"%v"}}`,
		testPuid,
	)
	errorPayloadResponse :=
		&payload.BytesPayload{
			Msg:         []byte(errorJson),
			ContentType: "application/json",
		}
	errorDelivery := rabbitmq.Delivery{
		Delivery: amqp.Delivery{
			ContentType:  "application/json",
			DeliveryMode: rabbitmq.Persistent,
			Body:         []byte(errorJson),
			Headers:      testHeaders,
		},
	}

	t.Run("create server", func(t *testing.T) {
		server, err := CreateRabbitMQServer(RabbitMQServerOptions{
			DeploymentName:  deploymentName,
			Namespace:       namespace,
			Protocol:        protocol,
			Transport:       transport,
			Annotations:     map[string]string{},
			ServerUrl:       *serverUrl,
			Predictor:       p,
			BrokerUrl:       brokerUrl,
			InputQueueName:  inputQueue,
			OutputQueueName: outputQueue,
			Log:             log,
			FullHealthCheck: fullHealthCheck,
		})

		assert.NoError(t, err)
		assert.NotNil(t, server)
		assert.NotNil(t, server.Client)
		assert.Equal(t, deploymentName, server.DeploymentName)
		assert.Equal(t, namespace, server.Namespace)
		assert.Equal(t, transport, server.Transport)
		assert.Equal(t, p, server.Predictor)
		assert.Equal(t, *serverUrl, server.ServerUrl)
		assert.Equal(t, brokerUrl, server.BrokerUrl)
		assert.Equal(t, inputQueue, server.InputQueueName)
		assert.Equal(t, outputQueue, server.OutputQueueName)
		assert.NotNil(t, server.Log)
		assert.Equal(t, protocol, server.Protocol)
		assert.Equal(t, fullHealthCheck, server.FullHealthCheck)
	})

	/*
	 * This makes sure the serve() code runs and makes the proper calls by setting up mocks.
	 * It is not doing anything to validate the messages are properly processed.  That's challenging in a
	 * unit test since the code connects to RabbitMQ.
	 */
	t.Run("serve", func(t *testing.T) {
		mockRmqConn := new(MockConnectionWrapper)

		mockPublisher := new(MockPublisherWrapper)
		mockRmqConn.On("NewPublisher").Return(mockPublisher, nil)
		mockPublisher.On("Close")

		testConsumer := new(TestConsumerWrapper)
		testConsumer.isClosed = false
		mockRmqConn.On(
			"NewConsumer",
			mock.Anything,
			inputQueue,
			mock.Anything,
		).Return(testConsumer, nil)

		wg := new(sync.WaitGroup)
		termChan, err := testServer.serve(mockRmqConn, wg)

		termChan <- true
		t.Log("waiting")
		wg.Wait()
		t.Log("done waiting")

		assert.NoError(t, err)

		mockRmqConn.AssertExpectations(t)
		mockPublisher.AssertExpectations(t)
		assert.True(t, testConsumer.isClosed)
	})

	t.Run("process valid request", func(t *testing.T) {
		mockRmqConn := new(MockConnectionWrapper)

		mockPublisher := new(MockPublisherWrapper)
		mockRmqConn.On("NewPublisher").Return(mockPublisher, nil)
		mockPublisher.On("Close")

		testConsumer := new(TestConsumerWrapper)
		testConsumer.isClosed = false
		mockRmqConn.On(
			"NewConsumer",
			mock.Anything,
			inputQueue,
			mock.Anything,
		).Return(testConsumer, nil)

		wg := new(sync.WaitGroup)
		termChan, err := testServer.serve(mockRmqConn, wg)

		mockPublisher.On("Publish", validPayloadResponseWithHeaders, outputQueue).Return(nil)
		action := testConsumer.SimulateDelivery(validRequestDelivery)
		assert.Equal(t, rabbitmq.Ack, action)

		termChan <- true
		t.Log("waiting")
		wg.Wait()
		t.Log("done waiting")

		assert.NoError(t, err)

		mockRmqConn.AssertExpectations(t)
		mockPublisher.AssertExpectations(t)
		assert.True(t, testConsumer.isClosed)
	})

	t.Run("process valid request missing puid", func(t *testing.T) {
		mockRmqConn := new(MockConnectionWrapper)

		mockPublisher := new(MockPublisherWrapper)
		mockRmqConn.On("NewPublisher").Return(mockPublisher, nil)
		mockPublisher.On("Close")

		testConsumer := new(TestConsumerWrapper)
		testConsumer.isClosed = false
		mockRmqConn.On(
			"NewConsumer",
			mock.Anything,
			inputQueue,
			mock.Anything,
		).Return(testConsumer, nil)

		wg := new(sync.WaitGroup)
		termChan, err := testServer.serve(mockRmqConn, wg)

		mockPublisher.On(
			"Publish",
			mock.MatchedBy(matchingSeldonPayloadsWithPuid(t, validPayloadResponseNoPuid, false)),
			outputQueue,
		).Return(nil)
		action := testConsumer.SimulateDelivery(validRequestNoPuidDelivery)
		assert.Equal(t, rabbitmq.Ack, action)

		termChan <- true
		t.Log("waiting")
		wg.Wait()
		t.Log("done waiting")

		assert.NoError(t, err)

		mockRmqConn.AssertExpectations(t)
		mockPublisher.AssertExpectations(t)
		assert.True(t, testConsumer.isClosed)
	})

	t.Run("process invalid request", func(t *testing.T) {
		mockRmqConn := new(MockConnectionWrapper)

		mockPublisher := new(MockPublisherWrapper)
		mockRmqConn.On("NewPublisher").Return(mockPublisher, nil)
		mockPublisher.On("Close")

		testConsumer := new(TestConsumerWrapper)
		testConsumer.isClosed = false
		mockRmqConn.On(
			"NewConsumer",
			mock.Anything,
			inputQueue,
			mock.Anything,
		).Return(testConsumer, nil)

		wg := new(sync.WaitGroup)
		termChan, err := testServer.serve(mockRmqConn, wg)

		// test that payload is expected and Puid is added
		mockPublisher.On(
			"Publish",
			mock.MatchedBy(matchingSeldonPayloadsWithPuid(t, invalidPayloadResponse, true)),
			outputQueue,
		).Return(nil)
		action := testConsumer.SimulateDelivery(invalidDelivery)
		assert.Equal(t, rabbitmq.NackDiscard, action)

		termChan <- true
		t.Log("waiting")
		wg.Wait()
		t.Log("done waiting")

		assert.NoError(t, err)

		mockRmqConn.AssertExpectations(t)
		mockPublisher.AssertExpectations(t)
		assert.True(t, testConsumer.isClosed)
	})

	t.Run("process error response", func(t *testing.T) {
		mockRmqConn := new(MockConnectionWrapper)

		mockPublisher := new(MockPublisherWrapper)
		mockRmqConn.On("NewPublisher").Return(mockPublisher, nil)
		mockPublisher.On("Close")

		testConsumer := new(TestConsumerWrapper)
		testConsumer.isClosed = false
		mockRmqConn.On(
			"NewConsumer",
			mock.Anything,
			inputQueue,
			mock.Anything,
		).Return(testConsumer, nil)

		wg := new(sync.WaitGroup)
		termChan, err := testServer.serve(mockRmqConn, wg)

		mockPublisher.On(
			"Publish",
			mock.MatchedBy(matchingSeldonPayloadsWithPuid(t, errorPayloadResponse, true)),
			outputQueue,
		).Return(nil)
		action := testConsumer.SimulateDelivery(errorDelivery)
		assert.Equal(t, rabbitmq.Ack, action)

		termChan <- true
		t.Log("waiting")
		wg.Wait()
		t.Log("done waiting")

		assert.NoError(t, err)

		mockRmqConn.AssertExpectations(t)
		mockPublisher.AssertExpectations(t)
		assert.True(t, testConsumer.isClosed)
	})
}

func addPuid(pl payload.SeldonPayload, puid string) (payload.SeldonPayload, error) {
	switch pl.GetContentType() {
	case rest.ContentTypeJSON:
		requestBody := &proto.SeldonMessage{}
		err := jsonpb.UnmarshalString(string(pl.GetPayload().([]byte)), requestBody)
		if err != nil {
			return nil, err
		}
		requestBody.Meta = &proto.Meta{
			Puid: puid,
		}
		ma := jsonpb.Marshaler{}
		marshaled, err := ma.MarshalToString(requestBody)
		if err != nil {
			return nil, err
		}
		return &payload.BytesPayload{
			Msg:         []byte(marshaled),
			ContentType: rest.ContentTypeJSON,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported content type '%v'", pl.GetContentType())
	}
}

func samePayloads(pl1 payload.SeldonPayload, pl2 payload.SeldonPayload) bool {
	providedBytes, _ := pl1.GetBytes()
	referenceBytes, _ := pl2.GetBytes()
	return bytes.Equal(providedBytes, referenceBytes) &&
		pl1.GetContentType() == pl2.GetContentType() &&
		pl1.GetContentEncoding() == pl2.GetContentEncoding()
}

func matchingSeldonPayloadsWithPuid(
	t *testing.T,
	reference payload.SeldonPayload,
	addPuidToPayload bool,
) func(SeldonPayloadWithHeaders) bool {
	return func(pl SeldonPayloadWithHeaders) bool {
		puidHeader := pl.Headers[payload.SeldonPUIDHeader]
		var referencePayloadToUse payload.SeldonPayload
		var err error
		if addPuidToPayload {
			referencePayloadToUse, err = addPuid(reference, puidHeader[0])
			if err != nil {
				t.Logf("error adding puid %v", err)
				return false
			}
		} else {
			referencePayloadToUse = reference
		}
		return puidHeader != nil && samePayloads(pl.Payload, referencePayloadToUse)
	}
}
