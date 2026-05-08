package billing

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// ArchiveDeps holds the dependencies injected into the archive activities.
// Constructed in cmd/workflow-worker and passed to Bundle. Pass nil to skip
// archive workflow + activity registration (e.g. when real deps such as the
// object store are not yet wired).
type ArchiveDeps struct {
	Detach *DetachPartitionActivity
	Export *ExportActivity
	Emit   *EmitArchiveEventActivity
	Drop   *DropPartitionActivity
}

// Bundle returns the registration set for the billing-maintenance task queue.
// Hosts the partition-rollover workflow (#18-rollover) and, when archive is
// non-nil, the partition-archive workflow (#69) plus its activities.
func Bundle(rollover *CreatePartitionActivity, archive *ArchiveDeps) gswf.Bundle {
	wfs := []any{PartitionRolloverWorkflow}
	acts := []any{
		gswf.NamedActivity{Name: ActivityNameCreatePartition, Activity: rollover.Execute},
	}
	if archive != nil {
		wfs = append(wfs, PartitionArchiveWorkflow, ArchiveRouterWorkflow)
		acts = append(acts,
			gswf.NamedActivity{Name: ActivityNameDetachPartition, Activity: archive.Detach.Execute},
			gswf.NamedActivity{Name: ActivityNameExport, Activity: archive.Export.Execute},
			gswf.NamedActivity{Name: ActivityNameEmitArchiveEvent, Activity: archive.Emit.Execute},
			gswf.NamedActivity{Name: ActivityNameDropPartition, Activity: archive.Drop.Execute},
		)
	}
	return gswf.Bundle{
		TaskQueue:  gswf.QueueBillingMaintenance,
		Workflows:  wfs,
		Activities: acts,
	}
}
