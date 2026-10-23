package kafka_test

import (
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/domain/network"
)

type (
	kafkaMessage = kafkago.Message
	network_Node = network.Node
)
