package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
)

const ActivityNameGlueRegister = "billing.GlueRegister"

// GlueClient narrows the AWS SDK Glue surface to the calls used by this
// activity, so tests can supply an in-memory fake without spinning up
// localstack. Mirrors the *glue.Client method signature.
type GlueClient interface {
	CreatePartition(ctx context.Context, in *glue.CreatePartitionInput, opts ...func(*glue.Options)) (*glue.CreatePartitionOutput, error)
}

// GlueRegisterInput is the input to GlueRegisterActivity.Execute.
//
// LakeURI is the s3:// URI of the partition's directory, e.g.
// s3://bucket/billing/usage_events/year=2026/month=05/.
// Year and Month identify the partition values registered with Glue.
type GlueRegisterInput struct {
	Year    int
	Month   int
	LakeURI string
}

// GlueRegisterActivity registers an archived monthly partition with the
// AWS Glue Data Catalog (database `gitscale_analytics`, table
// `usage_events`) so Athena queries against the analytics lake see archived
// months. Conforming to ADR-018 §Query path.
//
// Idempotent: glue.CreatePartition returns AlreadyExistsException on replay,
// which Execute treats as success.
type GlueRegisterActivity struct {
	client   GlueClient
	database string
	table    string
}

// NewGlueRegisterActivity constructs the activity. Returns an error if any
// argument is empty so the worker boot path fails fast (ADR-008/019 style).
func NewGlueRegisterActivity(client GlueClient, database, table string) (*GlueRegisterActivity, error) {
	if client == nil {
		return nil, errors.New("billing.NewGlueRegisterActivity: client is nil")
	}
	if database == "" {
		return nil, errors.New("billing.NewGlueRegisterActivity: database is empty")
	}
	if table == "" {
		return nil, errors.New("billing.NewGlueRegisterActivity: table is empty")
	}
	return &GlueRegisterActivity{client: client, database: database, table: table}, nil
}

// Hive parquet SerDe constants. Matches the table definition in
// terraform/analytics/main.tf — Athena requires these to read the archive.
const (
	parquetInputFormat  = "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"
	parquetOutputFormat = "org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"
	parquetSerde        = "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"
)

// Execute registers (year, month) → LakeURI with Glue. Treats
// AlreadyExistsException as success so retries / re-runs are safe.
func (a *GlueRegisterActivity) Execute(ctx context.Context, in GlueRegisterInput) error {
	if in.Year < 2026 || in.Year > 2099 {
		return fmt.Errorf("glue register: year %d out of range", in.Year)
	}
	if in.Month < 1 || in.Month > 12 {
		return fmt.Errorf("glue register: month %d out of range", in.Month)
	}
	if in.LakeURI == "" {
		return errors.New("glue register: LakeURI is empty")
	}

	yearStr := fmt.Sprintf("%04d", in.Year)
	monthStr := fmt.Sprintf("%02d", in.Month)

	input := &glue.CreatePartitionInput{
		DatabaseName: aws.String(a.database),
		TableName:    aws.String(a.table),
		PartitionInput: &gluetypes.PartitionInput{
			Values: []string{yearStr, monthStr},
			StorageDescriptor: &gluetypes.StorageDescriptor{
				Location:     aws.String(in.LakeURI),
				InputFormat:  aws.String(parquetInputFormat),
				OutputFormat: aws.String(parquetOutputFormat),
				SerdeInfo: &gluetypes.SerDeInfo{
					SerializationLibrary: aws.String(parquetSerde),
				},
			},
		},
	}

	if _, err := a.client.CreatePartition(ctx, input); err != nil {
		var alreadyExists *gluetypes.AlreadyExistsException
		if errors.As(err, &alreadyExists) {
			return nil
		}
		return fmt.Errorf("glue.CreatePartition: %w", err)
	}
	return nil
}
