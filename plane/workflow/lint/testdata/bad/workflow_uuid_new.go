// Fixture: deliberately violates determinism rules. See workflow_time_now.go.
package bad

import "github.com/google/uuid"

func WorkflowUUIDNew() {
	_ = uuid.New()
}
