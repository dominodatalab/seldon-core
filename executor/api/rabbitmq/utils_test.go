package rabbitmq

import (
	"github.com/golang/protobuf/jsonpb"
	proto2 "github.com/golang/protobuf/proto"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/seldonio/seldon-core/executor/api/grpc/seldon/proto"
	"github.com/seldonio/seldon-core/executor/api/payload"
	"github.com/seldonio/seldon-core/executor/api/rest"
	"github.com/stretchr/testify/assert"

	"testing"
)

func TestStringMapTableFunctions(t *testing.T) {
	origTable1 := amqp.Table{
		"key1": "value1",
		"key2": 45,
	}
	derivedStringMap1 := map[string][]string{
		"key1": {"value1"}p,
		"key2": {"45"},
	}
	stringMap1 := map[string][]string{
		"key1": {"value1", "value2"},
		"key2": {"45"},
	}
	derivedTable1 := amqp.Table{
		"key1": "value1",
		"key2": "45",
	}

	t.Run("TableToStringMap", func(t *testing.T) {
		mappedOrigTable1 := TableToStringMap(origTable1)
		assert.Equal(t, derivedStringMap1, mappedOrigTable1)
	})

	t.Run("StringMapToTable", func(t *testing.T) {
		mappedStringMap1 := StringMapToTable(stringMap1)
		assert.Equal(t, derivedTable1, mappedStringMap1)
	})
}

func TestDeliveryToPayload(t *testing.T) {
	bytesBody := []byte(`{"status":{"status":0},"strData":"\"hello\""}`)
	testDeliveryRest := amqp.Delivery{
		Body:            bytesBody,
		ContentType:     rest.ContentTypeJSON,
		ContentEncoding: "",
	}
	protoMessage := &proto.SeldonMessage{
		Status: &proto.Status{
			Status: proto.Status_SUCCESS,
		},
		Meta: nil,
		DataOneof: &proto.SeldonMessage_StrData{
			StrData: `"hello"`,
		},
	}
	protoMessageEnc, _ := proto2.Marshal(protoMessage)
	protoMessage.XXX_sizecache = 0 // to make test cases match
	testDeliveryProto := amqp.Delivery{
		Body:            protoMessageEnc,
		ContentType:     payload.APPLICATION_TYPE_PROTOBUF,
		ContentEncoding: "",
	}

	t.Run("proto payload", func(t *testing.T) {
		pl, err := DeliveryToPayload(testDeliveryProto)

		assert.NoError(t, err)
		assert.Equal(t, protoMessage, pl.GetPayload())
	})

	t.Run("rest payload", func(t *testing.T) {
		pl, err := DeliveryToPayload(testDeliveryRest)

		assert.NoError(t, err)
		assert.Equal(t, bytesBody, pl.GetPayload())

		body := &proto.SeldonMessage{}
		err = jsonpb.UnmarshalString(string(pl.GetPayload().([]byte)), body)

		assert.NoError(t, err)
		assert.Equal(t, protoMessage, body)
	})
}

func TestUpdatePayloadWithPuid(t *testing.T) {
	strMessage := `"goodbye"`
	puid := "123"

	t.Run("Update Meta with Puid when meta is nil", func(t *testing.T) {
		protoMessage := &proto.SeldonMessage{
			Status: &proto.Status{
				Status: proto.Status_SUCCESS,
			},
			Meta: nil,
			DataOneof: &proto.SeldonMessage_StrData{
				StrData: strMessage,
			},
		}
		msg, _ := new(jsonpb.Marshaler).MarshalToString(protoMessage)
		oldPayload := &payload.BytesPayload{Msg: []byte(msg), ContentType: rest.ContentTypeJSON, ContentEncoding: ""}

		updatedPayload, err := UpdatePayloadWithPuid(oldPayload, puid)
		assert.NoError(t, err)

		updatedMessage := &proto.SeldonMessage{}
		err2 := jsonpb.UnmarshalString(string(updatedPayload.GetPayload().([]byte)), updatedMessage)
		assert.NoError(t, err2)

		expectedMessage := &proto.SeldonMessage{
			Status: &proto.Status{
				Status: proto.Status_SUCCESS,
			},
			Meta: &proto.Meta{
				Puid: puid,
			},
			DataOneof: &proto.SeldonMessage_StrData{
				StrData: strMessage,
			},
		}

		assert.Equal(t, updatedMessage, expectedMessage)
	})

	t.Run("Do not update Meta with Puid when meta is not nil", func(t *testing.T) {
		protoMessage := &proto.SeldonMessage{
			Status: &proto.Status{
				Status: proto.Status_SUCCESS,
			},
			Meta: &proto.Meta{
				Puid: puid,
			},
			DataOneof: &proto.SeldonMessage_StrData{
				StrData: strMessage,
			},
		}
		msg, _ := new(jsonpb.Marshaler).MarshalToString(protoMessage)
		oldPayload := &payload.BytesPayload{Msg: []byte(msg), ContentType: rest.ContentTypeJSON, ContentEncoding: ""}

		updatedPayload, err := UpdatePayloadWithPuid(oldPayload, "789")
		assert.NoError(t, err)

		updatedMessage := &proto.SeldonMessage{}
		err2 := jsonpb.UnmarshalString(string(updatedPayload.GetPayload().([]byte)), updatedMessage)
		assert.NoError(t, err2)

		assert.Equal(t, protoMessage, updatedMessage)
		assert.NotEqual(t, updatedMessage.Meta.Puid, "789")
	})
}
