package billing

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
)

const ActivityNameEmitArchiveEvent = "billing.EmitArchiveEvent"

// EmitInput is the input to EmitArchiveEventActivity.Execute.
type EmitInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
}

// EmitArchiveEventActivity calls appclient.BillingClient.RecordPartitionArchived,
// which routes to the billing app-plane service (ADR-019). The service writes
// billing.partition_archived to billing_outbox in a single transaction (ADR-008).
type EmitArchiveEventActivity struct {
	client appclient.BillingClient
}

// NewEmitArchiveEventActivity returns an EmitArchiveEventActivity. Returns an
// error if client is nil so the worker boot path fails fast.
func NewEmitArchiveEventActivity(client appclient.BillingClient) (*EmitArchiveEventActivity, error) {
	if client == nil {
		return nil, errors.New("billing.NewEmitArchiveEventActivity: client is nil")
	}
	return &EmitArchiveEventActivity{client: client}, nil
}

// Execute calls RecordPartitionArchived on the billing app-plane service.
func (a *EmitArchiveEventActivity) Execute(ctx context.Context, in EmitInput) error {
	return a.client.RecordPartitionArchived(ctx, appclient.PartitionArchivedInput{
		Year:          in.Year,
		Month:         in.Month,
		PartitionName: in.PartitionName,
		LakeURI:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
	})
}
