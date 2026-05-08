// partition-rollover-trigger starts the PartitionRolloverWorkflow with a
// RunTime anchor that resolves to the requested target (year, month).
// Idempotent because CreatePartitionActivity is.
//
// Used by the oncall when the BillingUsageEventsNextPartitionMissing alert
// pages. See docs/runbooks/billing-partition-gap.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	billingwf "github.com/gitscale-platform/gitscale/plane/workflow/billing"
	"go.temporal.io/sdk/client"
)

func main() {
	year := flag.Int("year", 0, "target calendar year for the partition (e.g. 2027)")
	month := flag.Int("month", 0, "target calendar month 1..12")
	addr := flag.String("addr", getenv("TEMPORAL_ADDR", "localhost:7233"), "Temporal frontend address")
	namespace := flag.String("namespace", getenv("TEMPORAL_NAMESPACE", "default"), "Temporal namespace")
	taskQueue := flag.String("task-queue", getenv("WORKFLOW_TASK_QUEUE", "billing"), "Workflow task queue")
	flag.Parse()

	if *year < 2026 || *year > 2100 {
		fmt.Fprintln(os.Stderr, "--year must be between 2026 and 2100")
		os.Exit(2)
	}
	if *month < 1 || *month > 12 {
		fmt.Fprintln(os.Stderr, "--month must be between 1 and 12")
		os.Exit(2)
	}

	// PartitionRolloverWorkflow takes a RunTime anchor and creates the
	// partition for the calendar month immediately following the anchor's
	// month (see plane/workflow/billing/workflow.go nextMonthFrom). To target
	// (year, month) we anchor at the 15th of the prior month.
	runTime := priorMonthAnchor(*year, *month)

	c, err := client.Dial(client.Options{HostPort: *addr, Namespace: *namespace})
	if err != nil {
		log.Fatalf("temporal dial: %v", err)
	}
	defer c.Close()

	id := fmt.Sprintf("partition-rollover-manual-%04d-%02d-%d", *year, *month, time.Now().Unix())
	we, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        id,
		TaskQueue: *taskQueue,
	}, billingwf.PartitionRolloverWorkflow, billingwf.PartitionRolloverInput{
		RunTime: runTime,
	})
	if err != nil {
		log.Fatalf("execute workflow: %v", err)
	}
	log.Printf("started workflow id=%s run_id=%s anchor=%s target=%04d-%02d",
		we.GetID(), we.GetRunID(), runTime.Format(time.RFC3339), *year, *month)
	if err := we.Get(context.Background(), nil); err != nil {
		log.Fatalf("workflow result: %v", err)
	}
	log.Printf("ok: partition for %04d-%02d ensured", *year, *month)
}

// priorMonthAnchor returns the 15th of the calendar month preceding (year, month)
// in UTC. This is the deterministic anchor passed as RunTime so that the
// workflow's internal nextMonthFrom returns exactly (year, month).
func priorMonthAnchor(year, month int) time.Time {
	pm := month - 1
	py := year
	if pm == 0 {
		pm = 12
		py--
	}
	return time.Date(py, time.Month(pm), 15, 0, 0, 0, 0, time.UTC)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
