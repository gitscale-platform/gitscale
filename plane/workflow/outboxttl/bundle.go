package outboxttl

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Bundle returns the registration set for the outbox TTL expirer on the
// QueueBillingMaintenance task queue. The activity is registered under
// ActivityNameExpireDomainOutbox so the workflow can dispatch by name.
func Bundle(act *ExpireDomainOutboxActivity) gswf.Bundle {
	return gswf.Bundle{
		TaskQueue: gswf.QueueBillingMaintenance,
		Workflows: []any{ExpireOutboxesWorkflow},
		Activities: []any{
			gswf.NamedActivity{Name: ActivityNameExpireDomainOutbox, Activity: act.Execute},
		},
	}
}
