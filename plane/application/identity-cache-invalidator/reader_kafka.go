package invalidator

import (
	"context"

	kafkago "github.com/segmentio/kafka-go"
)

// kafkaReaderAdapter adapts *kafka.Reader to the consumer's MessageReader
// interface so the consumer package compiles without a hard dep on kafka-go.
type kafkaReaderAdapter struct {
	r *kafkago.Reader
}

// WrapKafkaReader returns a MessageReader backed by the given kafka.Reader.
func WrapKafkaReader(r *kafkago.Reader) MessageReader { return &kafkaReaderAdapter{r: r} }

func (a *kafkaReaderAdapter) FetchMessage(ctx context.Context) (RawMessage, error) {
	m, err := a.r.FetchMessage(ctx)
	if err != nil {
		return RawMessage{}, err
	}
	return RawMessage{
		Topic:     m.Topic,
		Partition: m.Partition,
		Offset:    m.Offset,
		Key:       m.Key,
		Value:     m.Value,
	}, nil
}

func (a *kafkaReaderAdapter) CommitMessages(ctx context.Context, msgs ...RawMessage) error {
	out := make([]kafkago.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, kafkago.Message{
			Topic:     m.Topic,
			Partition: m.Partition,
			Offset:    m.Offset,
			Key:       m.Key,
			Value:     m.Value,
		})
	}
	return a.r.CommitMessages(ctx, out...)
}

func (a *kafkaReaderAdapter) Close() error { return a.r.Close() }
