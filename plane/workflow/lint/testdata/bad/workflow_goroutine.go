// Fixture: deliberately violates determinism rules. See workflow_time_now.go.
package bad

func WorkflowGoroutine() {
	go func() {
		_ = 1 + 1
	}()
}
