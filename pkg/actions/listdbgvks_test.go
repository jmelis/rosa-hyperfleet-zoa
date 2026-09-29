package actions

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestListDBGVKs_Metadata_ItShouldBeAReadOnlyAWSAction(t *testing.T) {
	meta := (&listDBGVKs{}).Metadata()

	if meta.Name != "list_db_gvks" {
		t.Errorf("expected name list_db_gvks, got %q", meta.Name)
	}
	if meta.Scope != "aws-api" {
		t.Errorf("expected scope aws-api, got %q", meta.Scope)
	}
	if meta.Type != "read" {
		t.Errorf("expected type read, got %q", meta.Type)
	}
	if len(meta.Parameters) != 0 {
		t.Errorf("expected no parameters, got %v", meta.Parameters)
	}
	if len(meta.DeploymentTargets) != 1 || meta.DeploymentTargets[0] != DeploymentTargetRC {
		t.Errorf("expected deployment targets [%s], got %v", DeploymentTargetRC, meta.DeploymentTargets)
	}
}

func TestListDBGVKsValidate_WhenAWSConfigMissing_ItShouldError(t *testing.T) {
	if err := (&listDBGVKs{}).Validate(context.Background(), &ExecutionParams{}); err == nil {
		t.Fatal("expected error when AWSConfig is nil")
	}
}

func TestListDBGVKsValidate_WhenEnvVarMissing_ItShouldError(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	os.Unsetenv("HYPERFLEET_DB_USERNAME")

	params := &ExecutionParams{AWSConfig: &aws.Config{Region: "us-east-1"}}
	if err := (&listDBGVKs{}).Validate(context.Background(), params); err == nil {
		t.Fatal("expected error when HYPERFLEET_DB_USERNAME is unset")
	}
}

func TestListDBGVKsValidate_WhenReady_ItShouldPass(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	t.Setenv("HYPERFLEET_DB_USERNAME", "zoa_ro")

	params := &ExecutionParams{AWSConfig: &aws.Config{Region: "us-east-1"}}
	if err := (&listDBGVKs{}).Validate(context.Background(), params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListGVKsQuery_ItShouldGroupWithTombstoneFilter(t *testing.T) {
	if !strings.Contains(listGVKsQuery, "GROUP BY gvk") {
		t.Errorf("expected GROUP BY gvk, got: %s", listGVKsQuery)
	}
	if !strings.Contains(listGVKsQuery, "finalizers") {
		t.Errorf("expected tombstone filter, got: %s", listGVKsQuery)
	}
}
