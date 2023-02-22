package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/golang/protobuf/jsonpb"
	guuid "github.com/google/uuid"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/seldonio/seldon-core/executor/api/grpc/seldon/proto"
	"github.com/seldonio/seldon-core/executor/k8s"
	"github.com/wagslane/go-rabbitmq"

	"github.com/go-logr/logr"
	"github.com/seldonio/seldon-core/executor/api"
	"github.com/seldonio/seldon-core/executor/api/client"
	"github.com/seldonio/seldon-core/executor/api/grpc/seldon"
	"github.com/seldonio/seldon-core/executor/api/grpc/tensorflow"
	"github.com/seldonio/seldon-core/executor/api/payload"
	"github.com/seldonio/seldon-core/executor/api/rest"
	pred "github.com/seldonio/seldon-core/executor/predictor"
	v1 "github.com/seldonio/seldon-core/operator/apis/machinelearning.seldon.io/v1"
)

/*
 * based on `kafka/server.go`
 */

const (
	ENV_RABBITMQ_BROKER_URL    = "RABBITMQ_BROKER_URL"
	ENV_RABBITMQ_INPUT_QUEUE   = "RABBITMQ_INPUT_QUEUE"
	ENV_RABBITMQ_OUTPUT_QUEUE  = "RABBITMQ_OUTPUT_QUEUE"
	ENV_RABBITMQ_FULL_GRAPH    = "RABBITMQ_FULL_GRAPH"
	UNHANDLED_ERROR            = "Unhandled error from predictor process"
	DEFAULT_MAX_MSG_SIZE_BYTES = 10240
)

type SeldonRabbitMQServer struct {
	Client          client.SeldonApiClient
	DeploymentName  string
	Namespace       string
	Protocol        string
	Transport       string
	ServerUrl       url.URL
	Predictor       v1.PredictorSpec
	BrokerUrl       string
	InputQueueName  string
	OutputQueueName string
	Log             logr.Logger
	FullHealthCheck bool
}

type RabbitMQServerOptions struct {
	FullGraph       bool
	DeploymentName  string
	Namespace       string
	Protocol        string
	Transport       string
	Annotations     map[string]string
	ServerUrl       url.URL
	Predictor       v1.PredictorSpec
	BrokerUrl       string
	InputQueueName  string
	OutputQueueName string
	Log             logr.Logger
	FullHealthCheck bool
}

func CreateRabbitMQServer(args RabbitMQServerOptions) (*SeldonRabbitMQServer, error) {
	deploymentName, protocol, transport, annotations, predictor, log :=
		args.DeploymentName, args.Protocol, args.Transport, args.Annotations, args.Predictor, args.Log

	var apiClient client.SeldonApiClient
	var err error
	if args.FullGraph {
		err = errors.New("full graph not currently supported")
		log.Error(err, "tried to use full graph mode but not currently supported for RabbitMQ server")
		return nil, err
	}

	switch args.Transport {
	case api.TransportRest:
		log.Info("Start http rabbitmq graph")
		apiClient, err = rest.NewJSONRestClient(protocol, deploymentName, &predictor, annotations)
		if err != nil {
			log.Error(err, "error creating json rest client")
			return nil, fmt.Errorf("error '%w' creating json rest client", err)
		}
	case api.TransportGrpc:
		log.Info("Start grpc rabbitmq graph")
		if protocol == "seldon" {
			apiClient = seldon.NewSeldonGrpcClient(&predictor, deploymentName, annotations)
		} else {
			apiClient = tensorflow.NewTensorflowGrpcClient(&predictor, deploymentName, annotations)
		}
	default:
		return nil, fmt.Errorf("unknown transport '%s'", transport)
	}

	return &SeldonRabbitMQServer{
		Client:          apiClient,
		DeploymentName:  deploymentName,
		Namespace:       args.Namespace,
		Transport:       transport,
		Predictor:       predictor,
		ServerUrl:       args.ServerUrl,
		BrokerUrl:       args.BrokerUrl,
		InputQueueName:  args.InputQueueName,
		OutputQueueName: args.OutputQueueName,
		Log:             log.WithName("RabbitMQServer"),
		Protocol:        protocol,
		FullHealthCheck: args.FullHealthCheck,
	}, nil
}

func (rs *SeldonRabbitMQServer) Serve() error {
	conn, err := createRabbitMQConnection(rs.BrokerUrl, rs.Log)
	if err != nil {
		rs.Log.Error(err, "error connecting to rabbitmq")
		return fmt.Errorf("error '%w' connecting to rabbitmq", err)
	}
	defer func(conn ConnectionWrapper) {
		err := conn.Close()
		if err != nil {
			rs.Log.Error(err, "error closing rabbitMQ connection")
		}
	}(conn)

	wg := new(sync.WaitGroup)
	terminateChan, err := rs.serve(conn, wg)
	if err != nil {
		rs.Log.Error(err, "error starting rabbitmq server")
		return fmt.Errorf("error '%w' starting rabbitmq server", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	wg.Add(1)
	// wait for shutdown signal and terminate if received
	go func() {
		rs.Log.Info("awaiting OS shutdown signals")
		sig := <-sigs
		rs.Log.Info("sending termination message due to signal", "signal", sig)
		terminateChan <- true
		wg.Done()
	}()

	wg.Wait()

	rs.Log.Info("RabbitMQ server terminated normally")
	return nil
}

func (rs *SeldonRabbitMQServer) serve(conn ConnectionWrapper, wg *sync.WaitGroup) (chan<- bool, error) {
	// not sure if this is the best pattern or better to pass in pod name explicitly somehow
	consumerTag, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("error '%w' retrieving hostname", err)
	}

	publisher, err := conn.NewPublisher()
	if err != nil {
		return nil, fmt.Errorf("error '%w' creating RMQ publisher", err)
	}
	rs.Log.Info("Created", "publisher", publisher)

	consumerHandler := CreateConsumerHandler(
		func(reqPl *SeldonPayloadWithHeaders) error { return rs.predictAndPublishResponse(reqPl, publisher) },
		func(args ConsumerError) error { return rs.createAndPublishErrorResponse(args, publisher) },
		rs.Log,
	)

	// wait for graph to be ready
	ready := false
	for ready == false {
		err := pred.Ready(rs.Protocol, &rs.Predictor.Graph, rs.FullHealthCheck)
		ready = err == nil
		if !ready {
			rs.Log.Info("Waiting for graph to be ready")
			time.Sleep(2 * time.Second)
		}
	}

	consumer, err := conn.NewConsumer(consumerHandler, rs.InputQueueName, consumerTag)
	if err != nil {
		return nil, fmt.Errorf("error '%w' creating RMQ consumer", err)
	}
	rs.Log.Info("Created", "consumer", consumer, "input queue", rs.InputQueueName)

	// provide a channel to terminate the server
	terminate := make(chan bool, 1)

	wg.Add(1)
	go func() {
		rs.Log.Info("awaiting group termination")
		<-terminate
		rs.Log.Info("termination initiated, shutting down")
		consumer.Close()
		publisher.Close()
		wg.Done()
	}()

	return terminate, nil
}

func (rs *SeldonRabbitMQServer) predictAndPublishResponse(
	reqPayload *SeldonPayloadWithHeaders,
	publisher PublisherWrapper,
) error {
	if reqPayload == nil {
		err := errors.New("missing request payload")
		rs.Log.Error(err, "passed in request payload was blank")
		return err
	}

	seldonPuid := assignAndReturnPUID(reqPayload, nil)

	ctx := context.WithValue(context.Background(), payload.SeldonPUIDHeader, seldonPuid)

	// Apply tracing if active
	if opentracing.IsGlobalTracerRegistered() {
		tracer := opentracing.GlobalTracer()
		serverSpan := tracer.StartSpan("rabbitMqServer", ext.RPCServerOption(nil))
		ctx = opentracing.ContextWithSpan(ctx, serverSpan)
		defer serverSpan.Finish()
	}

	rs.Log.Info("rabbitmq server values", "server url", rs.ServerUrl)
	seldonPredictorProcess := pred.NewPredictorProcess(
		ctx, rs.Client, rs.Log.WithName("RabbitMqClient"), &rs.ServerUrl, rs.Namespace, reqPayload.Headers, "")

	resPayload, err := seldonPredictorProcess.Predict(&rs.Predictor.Graph, reqPayload.Payload)
	if err != nil && resPayload == nil {
		// normal errors from the predict process contain a status failed payload
		// this is handling an unexpected case, so failing entirely, at least for now
		rs.Log.Error(err, UNHANDLED_ERROR)
		return fmt.Errorf("unhandled error %w from predictor process", err)
	}
	arrBytes, e := resPayload.GetBytes()
	if e != nil {
		return fmt.Errorf("error '%w' unmarshaling seldon payload", e)
	}
	annotations, _ := k8s.GetAnnotations()
	msgLimit := annotations[k8s.ANNOTATION_RABBITMQ_MAX_MESSAGE_SIZE]
	intMsgLimit, e := strconv.Atoi(msgLimit)
	if intMsgLimit == 0 || e != nil {
		rs.Log.Info("Failed to read maximum allowed message size defaulting to 10240 bytes", "msg-size-found", intMsgLimit)
		intMsgLimit = DEFAULT_MAX_MSG_SIZE_BYTES
	}
	rs.Log.Info("Maximum allowed message size for rabbitmq", "rabbitmq-max-message-size-in-bytes", intMsgLimit)
	if len(arrBytes) > intMsgLimit {
		message := &proto.SeldonMessage{
			Status: &proto.Status{
				Code:   -1,
				Info:   "Payload size is greater than allowed size",
				Reason: "payload is large",
				Status: proto.Status_FAILURE,
			},
		}
		jsonStr, err := new(jsonpb.Marshaler).MarshalToString(message)
		if err != nil {
			rs.Log.Error(err, "error marshaling seldon message")
			return fmt.Errorf("error '%w' marshaling seldon message", err)
		}
		resPayload = &payload.BytesPayload{
			Msg:             []byte(jsonStr),
			ContentType:     rest.ContentTypeJSON,
			ContentEncoding: "",
		}
	}

	updatedPayload, err := UpdatePayloadWithPuid(reqPayload.Payload, resPayload)
	if err != nil {
		rs.Log.Error(err, UNHANDLED_ERROR)
		return fmt.Errorf("unhandled error %w from predictor process", err)
	}
	return rs.publishPayload(publisher, updatedPayload, seldonPuid)
}

func (rs *SeldonRabbitMQServer) createAndPublishErrorResponse(errorArgs ConsumerError, publisher PublisherWrapper) error {
	reqPayload := errorArgs.pl

	seldonPuid := assignAndReturnPUID(reqPayload, &errorArgs.delivery)

	message := &proto.SeldonMessage{
		Status: &proto.Status{
			Code:   0,
			Info:   "Prediction Failed",
			Reason: errorArgs.err.Error(),
			Status: proto.Status_FAILURE,
		},
		Meta: &proto.Meta{
			Puid: seldonPuid,
		},
	}

	var resPayload payload.SeldonPayload
	switch errorArgs.delivery.ContentType {
	case payload.APPLICATION_TYPE_PROTOBUF:
		resPayload = &payload.ProtoPayload{Msg: message}
		break
	default: // includes `rest.ContentTypeJson`, defaulting to json response if unknown payload
		if errorArgs.delivery.ContentType != rest.ContentTypeJSON {
			err := fmt.Errorf("unknown content type %v", errorArgs.delivery.ContentType)
			rs.Log.Error(err, "unexpected content type", "default response content-type", rest.ContentTypeJSON)
		}
		jsonStr, err := new(jsonpb.Marshaler).MarshalToString(message)
		if err != nil {
			rs.Log.Error(err, "error marshaling seldon message")
			return fmt.Errorf("error '%w' marshaling seldon message", err)
		}
		resPayload = &payload.BytesPayload{
			Msg:         []byte(jsonStr),
			ContentType: rest.ContentTypeJSON,
		}
		break
	}

	return rs.publishPayload(publisher, resPayload, seldonPuid)
}

func addPuidHeader(pl payload.SeldonPayload, seldonPuid string) SeldonPayloadWithHeaders {
	resHeaders := map[string][]string{payload.SeldonPUIDHeader: {seldonPuid}}
	return SeldonPayloadWithHeaders{
		pl,
		resHeaders,
	}
}

func (rs *SeldonRabbitMQServer) publishPayload(publisher PublisherWrapper, pl payload.SeldonPayload, seldonPuid string) error {
	plWithHeaders := addPuidHeader(pl, seldonPuid)
	return publisher.Publish(plWithHeaders, rs.OutputQueueName)
}

func assignAndReturnPUID(pl *SeldonPayloadWithHeaders, delivery *rabbitmq.Delivery) string {
	if pl == nil {
		if delivery != nil && delivery.Headers != nil && delivery.Headers[payload.SeldonPUIDHeader] != nil {
			return delivery.Headers[payload.SeldonPUIDHeader].(string)
		}
		return guuid.New().String()
	}
	if pl.Headers == nil {
		pl.Headers = make(map[string][]string)
	}
	if pl.Headers[payload.SeldonPUIDHeader] == nil {
		pl.Headers[payload.SeldonPUIDHeader] = []string{guuid.New().String()}
	}
	return pl.Headers[payload.SeldonPUIDHeader][0]
}
