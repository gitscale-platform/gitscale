// Fixture: passes determinism rules. Models a workflow that uses
// workflow.Now (would be) and avoids forbidden APIs entirely.
package good

func WorkflowClean(payload string) string {
	// No time.Now, no uuid.New, no goroutines, no channels — clean.
	return "processed:" + payload
}
