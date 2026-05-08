//go:build integration

package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// bootLocalstackGlue boots a localstack container with Glue enabled and
// returns a *glue.Client pointing at it. Pre-creates the
// gitscale_analytics.usage_events table with the same Hive partition spec
// that terraform/analytics/main.tf documents.
func bootLocalstackGlue(t *testing.T) *glue.Client {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "localstack/localstack:3.5",
		ExposedPorts: []string{"4566/tcp"},
		Env: map[string]string{
			"SERVICES":      "glue",
			"DEFAULT_REGION": "us-east-1",
		},
		WaitingFor: wait.ForHTTP("/_localstack/health").
			WithPort("4566/tcp").
			WithStartupTimeout(90 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Fatalf("localstack container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "4566/tcp")
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	client := glue.NewFromConfig(cfg, func(o *glue.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	if _, err := client.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String("gitscale_analytics")},
	}); err != nil {
		t.Fatalf("create db: %v", err)
	}
	if _, err := client.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String("gitscale_analytics"),
		TableInput: &gluetypes.TableInput{
			Name: aws.String("usage_events"),
			PartitionKeys: []gluetypes.Column{
				{Name: aws.String("year"), Type: aws.String("string")},
				{Name: aws.String("month"), Type: aws.String("string")},
			},
			StorageDescriptor: &gluetypes.StorageDescriptor{
				Location:     aws.String("s3://gitscale-analytics-lake/billing/usage_events/"),
				InputFormat:  aws.String(parquetInputFormat),
				OutputFormat: aws.String(parquetOutputFormat),
				SerdeInfo: &gluetypes.SerDeInfo{
					SerializationLibrary: aws.String(parquetSerde),
				},
				Columns: []gluetypes.Column{
					{Name: aws.String("event_id"), Type: aws.String("string")},
					{Name: aws.String("ts"), Type: aws.String("timestamp")},
				},
			},
			TableType: aws.String("EXTERNAL_TABLE"),
		},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return client
}

func TestGlueRegisterActivity_Integration_LocalstackRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	client := bootLocalstackGlue(t)
	a, err := NewGlueRegisterActivity(client, "gitscale_analytics", "usage_events")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := GlueRegisterInput{
		Year:    2026,
		Month:   5,
		LakeURI: "s3://gitscale-analytics-lake/billing/usage_events/year=2026/month=05/",
	}
	ctx := context.Background()
	if err := a.Execute(ctx, in); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, err := client.GetPartition(ctx, &glue.GetPartitionInput{
		DatabaseName:    aws.String("gitscale_analytics"),
		TableName:       aws.String("usage_events"),
		PartitionValues: []string{"2026", "05"},
	})
	if err != nil {
		t.Fatalf("GetPartition: %v", err)
	}
	if got.Partition == nil || got.Partition.StorageDescriptor == nil {
		t.Fatalf("partition or storage descriptor nil: %+v", got)
	}
	if loc := aws.ToString(got.Partition.StorageDescriptor.Location); loc != in.LakeURI {
		t.Errorf("location=%q want %q", loc, in.LakeURI)
	}

	// Re-run: AlreadyExists must be swallowed.
	if err := a.Execute(ctx, in); err != nil {
		t.Errorf("idempotent re-run failed: %v", err)
	}
}
