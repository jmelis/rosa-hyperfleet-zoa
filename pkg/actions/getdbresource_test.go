package actions

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func dbTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestGetDBResource_Metadata_ItShouldBeAReadOnlyAWSAction(t *testing.T) {
	meta := (&getDBResource{}).Metadata()

	if meta.Name != "get_db_resource" {
		t.Errorf("expected name get_db_resource, got %q", meta.Name)
	}
	if meta.Scope != "aws-api" {
		t.Errorf("expected scope aws-api, got %q", meta.Scope)
	}
	if meta.Type != "read" {
		t.Errorf("expected type read, got %q", meta.Type)
	}
	if len(meta.DeploymentTargets) != 1 || meta.DeploymentTargets[0] != DeploymentTargetRC {
		t.Errorf("expected deployment targets [%s], got %v", DeploymentTargetRC, meta.DeploymentTargets)
	}
}

func TestGetDBResourceValidate_WhenAWSConfigMissing_ItShouldError(t *testing.T) {
	err := (&getDBResource{}).Validate(context.Background(), &ExecutionParams{})
	if err == nil {
		t.Fatal("expected error when AWSConfig is nil")
	}
}

func TestGetDBResourceValidate_WhenEnvVarMissing_ItShouldError(t *testing.T) {
	// Only set two of the three required vars; the third must trigger an error.
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	os.Unsetenv("HYPERFLEET_DB_USERNAME")

	params := &ExecutionParams{AWSConfig: &aws.Config{Region: "us-east-1"}}
	err := (&getDBResource{}).Validate(context.Background(), params)
	if err == nil {
		t.Fatal("expected error when HYPERFLEET_DB_USERNAME is unset")
	}
}

func TestGetDBResourceValidate_WhenAllRequiredEnvSet_ItShouldPass(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	t.Setenv("HYPERFLEET_DB_USERNAME", "zoa_ro")

	params := &ExecutionParams{AWSConfig: &aws.Config{Region: "us-east-1"}}
	if err := (&getDBResource{}).Validate(context.Background(), params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
