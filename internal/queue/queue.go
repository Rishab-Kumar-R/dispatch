package queue

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// Message is the SQS body. It carries only the delivery ID: the worker loads
// current endpoint state from the DB, so a message can never go stale.
type Message struct {
	DeliveryID uuid.UUID `json:"delivery_id"`
}

func Decode(body string) (uuid.UUID, error) {
	var m Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return uuid.Nil, fmt.Errorf("decode queue message: %w", err)
	}
	if m.DeliveryID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("decode queue message: missing delivery_id")
	}

	return m.DeliveryID, nil
}

type SQS struct {
	client *sqs.Client
	url    string
}

func NewSQS(client *sqs.Client, queueURL string) *SQS {
	return &SQS{
		client: client,
		url:    queueURL,
	}
}

// Publish sends delivery IDs in batches of 10. If any message fails it returns
// an error and the caller leaves the rows unqueued: the relay re-publishes them
// later, so some messages may arrive twice. The worker's guards make that harmless.
func (p *SQS) Publish(ctx context.Context, ids []uuid.UUID) error {
	for start := 0; start < len(ids); start += 10 {
		end := min(start+10, len(ids))
		entries := make([]types.SendMessageBatchRequestEntry, 0, end-start)

		for i, id := range ids[start:end] {
			body, err := json.Marshal(Message{
				DeliveryID: id,
			})
			if err != nil {
				return err
			}

			entries = append(entries, types.SendMessageBatchRequestEntry{
				Id:          aws.String(strconv.Itoa(i)),
				MessageBody: aws.String(string(body)),
			})
		}

		out, err := p.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
			QueueUrl: aws.String(p.url),
			Entries:  entries,
		})
		if err != nil {
			return fmt.Errorf("sqs send batch: %w", err)
		}

		if n := len(out.Failed); n > 0 {
			return fmt.Errorf("sqs send batch: %d of %d messages failed (first: %s)", n, len(entries), aws.ToString(out.Failed[0].Message))
		}
	}

	return nil
}
