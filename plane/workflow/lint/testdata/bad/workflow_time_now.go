// This file is a fixture for plane/workflow/lint. It deliberately violates
// the determinism rules — DO NOT compile or import. testdata/ is ignored by
// the Go toolchain so this never reaches go build.
package bad

import "time"

func WorkflowTimeNow() {
	_ = time.Now()
}
