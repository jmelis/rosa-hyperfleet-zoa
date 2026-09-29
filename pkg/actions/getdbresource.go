package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/jackc/pgx/v5"
)

func init() {
	Register(&getDBResource{})
}

type getDBResource struct{}

func (a *getDBResource) Metadata() ActionMetadata {
	return ActionMetadata{
		Name:          "get_db_resource",
		Scope:         "aws-api",
		Type:          "read",
		ExecutionMode: "sync",
		Description:   "Get or list HyperFleet control-plane resources directly from the hyperfleet-db (Aurora PostgreSQL) by GVK, namespace, and name.",
		Authorization: AuthorizationConfig{Approval: "none"},
		// RC only: hyperfleet-db lives in the regional cluster VPC.
		DeploymentTargets: []string{DeploymentTargetRC},
		TimeoutSeconds:    30,
		Parameters: []ParameterDef{
			{Name: "gvk", Required: true, Description: "Group/Version/Kind, e.g. hypershift.openshift.io/v1beta1/HostedCluster (core group: /v1/ConfigMap)"},
			{Name: "namespace", Description: "Target namespace (omit with all_namespaces, or for cluster-scoped resources)"},
			{Name: "all_namespaces", Default: "false", Description: "List across all namespaces (ignores namespace)"},
			{Name: "name", Description: "Get a specific resource by name"},
		},
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

	gvk := strings.TrimSpace(params.Params["gvk"])
	if gvk == "" {
		return fmt.Errorf("parameter 'gvk' is required")
	}
	params.Params["gvk"] = gvk
	if err := validateGVK(gvk); err != nil {
		return err
	}

	return nil
}

func (a *getDBResource) Execute(ctx context.Context, params *ExecutionParams) (*ActionResult, error) {
	gvk := params.Params["gvk"]
	namespace := params.Params["namespace"]
	name := params.Params["name"]
	allNamespaces := params.Params["all_namespaces"] == "true"

	conn, err := connectHyperfleetDB(ctx, params)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	query, args := buildListQuery(gvk, namespace, name, allNamespaces)

	params.Logger.Info("querying hyperfleet-db",
		"gvk", gvk,
		"namespace", namespace,
		"name", name,
		"all_namespaces", allNamespaces,
	)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query resources: %w", err)
	}
	defer rows.Close()

	items := make([]map[string]interface{}, 0)
	for rows.Next() {
		var (
			gvkVal, ns, nm, uid         string
			objectVersion               int64
			specRaw, statusRaw, metaRaw []byte
			delTS, createdAt, updatedAt *string
		)
		if err := rows.Scan(
			&gvkVal, &ns, &nm, &uid, &objectVersion,
			&specRaw, &statusRaw, &metaRaw,
			&delTS, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan resource row: %w", err)
		}
		items = append(items, map[string]interface{}{
			"gvk":               gvkVal,
			"namespace":         ns,
			"name":              nm,
			"uid":               uid,
			"objectVersion":     objectVersion,
			"metadata":          decodeJSONB(metaRaw),
			"spec":              decodeJSONB(specRaw),
			"status":            decodeJSONB(statusRaw),
			"deletionTimestamp": delTS,
			"createdAt":         createdAt,
			"updatedAt":         updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed reading resource rows: %w", err)
	}

	scope := namespace
	if allNamespaces || namespace == "" {
		scope = "all namespaces"
	}

	// A specific name lookup returns the single object (or a not-found error),
	// mirroring `kubectl get <kind> <name>`.
	if name != "" {
		if len(items) == 0 {
			return nil, fmt.Errorf("%s %q not found in %s", gvk, name, scope)
		}
		if len(items) == 1 {
			return &ActionResult{
				Success: true,
				Output:  items[0],
				Summary: fmt.Sprintf("Retrieved %s %s", gvk, name),
			}, nil
		}
	}

	return &ActionResult{
		Success: true,
		Output:  items,
		Summary: fmt.Sprintf("Found %d %s in %s", len(items), gvk, scope),
	}, nil
}

// connectHyperfleetDB opens a pgx connection to hyperfleet-db using an RDS IAM
// auth token as the password. The token is a presigned URL containing reserved
// characters, so it is set on the config's Password field verbatim rather than
// interpolated into the DSN (which would corrupt it).
func connectHyperfleetDB(ctx context.Context, params *ExecutionParams) (*pgx.Conn, error) {
	endpoint := os.Getenv("HYPERFLEET_DB_ENDPOINT")
	dbName := os.Getenv("HYPERFLEET_DB_NAME")
	username := os.Getenv("HYPERFLEET_DB_USERNAME")
	region := params.AWSConfig.Region

	authToken, err := auth.BuildAuthToken(ctx, endpoint, region, username, params.AWSConfig.Credentials)
	if err != nil {
		return nil, fmt.Errorf("failed to build IAM auth token: %w", err)
	}

	connConfig, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require",
		username,
		endpoint,
		dbName,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}
	connConfig.Password = authToken

	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	return conn, nil
}

// buildListQuery constructs the SELECT against kubernetes_resources for the
// given filters. The tombstone filter (deletion_timestamp set with no
// finalizers) matches the hyperfleet-operator's own List query so fully-deleted
// objects are excluded while dying objects (still holding finalizers) are shown.
func buildListQuery(gvk, namespace, name string, allNamespaces bool) (string, []any) {
	var qb strings.Builder
	qb.WriteString(`SELECT gvk, namespace, name, uid::text, object_version, ` +
		`spec, status, metadata, ` +
		`deletion_timestamp::text, created_at::text, updated_at::text ` +
		`FROM kubernetes_resources ` +
		`WHERE gvk = $1 ` +
		`AND (deletion_timestamp IS NULL OR metadata->'finalizers' != '[]'::jsonb)`)
	args := []any{gvk}

	if !allNamespaces && namespace != "" {
		args = append(args, namespace)
		fmt.Fprintf(&qb, " AND namespace = $%d", len(args))
	}
	if name != "" {
		args = append(args, name)
		fmt.Fprintf(&qb, " AND name = $%d", len(args))
	}

	qb.WriteString(" ORDER BY namespace, name")
	return qb.String(), args
}

// validateGVK checks the group/version/kind format stored in the gvk column.
// The core group is empty, so a leading slash (e.g. "/v1/Pod") is valid.
func validateGVK(gvk string) error {
	parts := strings.Split(gvk, "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return fmt.Errorf("invalid gvk %q: expected Group/Version/Kind (e.g. apps/v1/Deployment or /v1/Pod)", gvk)
	}
	return nil
}

// decodeJSONB unmarshals a JSONB column into a generic value so it serializes as
// nested JSON in the result. Falls back to the raw string if it is not valid JSON.
func decodeJSONB(raw []byte) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}
