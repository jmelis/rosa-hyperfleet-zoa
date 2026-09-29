package actions

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/jackc/pgx/v5"
)

func init() {
	Register(&getDBResource{})
}

type getDBResource struct{}

func (a *getDBResource) Metadata() ActionMetadata {
	return ActionMetadata{
		Name:              "get_db_resource",
		Scope:             "aws-api",
		Type:              "read",
		ExecutionMode:     "sync",
		Description:       "Get Kubernetes resources from the HyperFleet database (connection test for now).",
		Authorization:     AuthorizationConfig{Approval: "none"},
		DeploymentTargets: []string{DeploymentTargetRC},
		TimeoutSeconds:    30,
		Parameters:        []ParameterDef{},
	}
}

func (a *getDBResource) Validate(_ context.Context, params *ExecutionParams) error {
	if params.AWSConfig == nil {
		return fmt.Errorf("AWS configuration is required")
	}

	// Check that required env vars are set
	requiredEnvVars := []string{"HYPERFLEET_DB_ENDPOINT", "HYPERFLEET_DB_NAME", "HYPERFLEET_DB_USERNAME"}
	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			return fmt.Errorf("environment variable %s is not set", envVar)
		}
	}

	return nil
}

func (a *getDBResource) Execute(ctx context.Context, params *ExecutionParams) (*ActionResult, error) {
	endpoint := os.Getenv("HYPERFLEET_DB_ENDPOINT")
	dbName := os.Getenv("HYPERFLEET_DB_NAME")
	username := os.Getenv("HYPERFLEET_DB_USERNAME")

	params.Logger.Info("testing database connection",
		"endpoint", endpoint,
		"database", dbName,
		"username", username)

	// Get AWS region from config
	region := params.AWSConfig.Region

	// Generate IAM auth token (15-minute validity)
	authToken, err := auth.BuildAuthToken(ctx, endpoint, region, username, params.AWSConfig.Credentials)
	if err != nil {
		return nil, fmt.Errorf("failed to build IAM auth token: %w", err)
	}

	params.Logger.Info("generated IAM auth token successfully")

	// Build the connection config from a DSN WITHOUT the token, then set the
	// password field verbatim. The RDS IAM auth token is a presigned URL
	// containing reserved characters (/, ?, &, =, %), so interpolating it into
	// a postgres://user:password@host URL corrupts it — the parser truncates
	// the password at the first '/'. Setting Password directly avoids any
	// URL-encoding pitfalls.
	connConfig, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require",
		username,
		endpoint,
		dbName,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}
	connConfig.Password = authToken

	// Connect to the database
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer conn.Close(ctx)

	params.Logger.Info("connected to database successfully")

	// Run a simple query to verify the connection works
	var version string
	err = conn.QueryRow(ctx, "SELECT version()").Scan(&version)
	if err != nil {
		return nil, fmt.Errorf("failed to query database: %w", err)
	}

	params.Logger.Info("database query successful", "version", version)

	// TODO: Implement actual resource fetching from kubernetes_resources table
	// Parameters will be: resource (GVK), namespace, name, all_namespaces

	return &ActionResult{
		Success: true,
		Output: map[string]interface{}{
			"endpoint":   endpoint,
			"database":   dbName,
			"username":   username,
			"version":    version,
			"connection": "success",
		},
		Summary: fmt.Sprintf("✅ Database connection successful (endpoint: %s)", endpoint),
	}, nil
}
