package billing

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// ArchiveDeps holds the dependencies injected into the archive activities.
// Constructed in cmd/workflow-worker and passed to Bundle. Pass nil to skip
// archive workflow + activity registration (e.g. when real deps such as the
// object store are not yet wired).
type ArchiveDeps struct {
	Detach       *DetachPartitionActivity
	Export       *ExportActivity
	Emit         *EmitArchiveEventActivity
	GlueRegister *GlueRegisterActivity
	Drop         *DropPartitionActivity
}

// RestoreDeps holds the dependencies injected into the restore activities.
// Constructed in cmd/workflow-worker and passed to Bundle. Pass nil to skip
// restore workflow + activity registration. Restore deps are independent of
// ArchiveDeps so a worker pool can be sized for read-heavy restore traffic
// without enabling archive scheduling.
type RestoreDeps struct {
	FetchManifest    *FetchManifestActivity
	VerifyChecksum   *VerifyChecksumActivity
	DownloadDecrypt  *DownloadAndDecryptActivity
	LoadQuarantine   *LoadIntoQuarantineActivity
	DropQuarantine   *DropQuarantineActivity
}

// Bundle returns the registration set for the billing-maintenance task queue.
// Hosts the partition-rollover workflow (#18-rollover) and, when archive is
// non-nil, the partition-archive workflow (#69) plus its activities; when
// restore is non-nil, the partition-restore workflow (#79) plus its
// activities.
func Bundle(rollover *CreatePartitionActivity, archive *ArchiveDeps, restore *RestoreDeps) gswf.Bundle {
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
			gswf.NamedActivity{Name: ActivityNameGlueRegister, Activity: archive.GlueRegister.Execute},
			gswf.NamedActivity{Name: ActivityNameDropPartition, Activity: archive.Drop.Execute},
		)
	}
	if restore != nil {
		wfs = append(wfs, RestorePartitionWorkflow)
		acts = append(acts,
			gswf.NamedActivity{Name: ActivityNameFetchManifest, Activity: restore.FetchManifest.Execute},
			gswf.NamedActivity{Name: ActivityNameVerifyChecksum, Activity: restore.VerifyChecksum.Execute},
			gswf.NamedActivity{Name: ActivityNameDownloadDecrypt, Activity: restore.DownloadDecrypt.Execute},
			gswf.NamedActivity{Name: ActivityNameLoadQuarantine, Activity: restore.LoadQuarantine.Execute},
			gswf.NamedActivity{Name: ActivityNameDropQuarantine, Activity: restore.DropQuarantine.Execute},
		)
	}
	return gswf.Bundle{
		TaskQueue:  gswf.QueueBillingMaintenance,
		Workflows:  wfs,
		Activities: acts,
	}
}
