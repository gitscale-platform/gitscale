package billing

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Bundle returns the registration set for the billing-maintenance task queue.
// Hosts the partition-rollover workflow + activity (#18-rollover); future
// archive workflow (#18-archive) will join this bundle.
func Bundle(activity *CreatePartitionActivity) gswf.Bundle {
	return gswf.Bundle{
		TaskQueue:  gswf.QueueBillingMaintenance,
		Workflows:  []any{PartitionRolloverWorkflow},
		Activities: []any{gswf.NamedActivity{Name: ActivityNameCreatePartition, Activity: activity.Execute}},
	}
}
