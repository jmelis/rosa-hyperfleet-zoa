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

// tombstoneFilter excludes fully-deleted rows (deletion_timestamp set with no
// finalizers) while keeping live and dying objects. It matches the
// hyperfleet-operator's own List query (hyperfleet-db/internal/reader/list.go).
const tombstoneFilter = `(deletion_timestamp IS NULL OR metadata->'finalizers' != '[]'::jsonb)`

func init() {
	Register(&getDBResource{})
}

// validateDBConnParams checks the prerequisites shared by all hyperfleet-db
// actions: an AWS config (for the IAM auth token) and the DB connection env vars.
func validateDBConnParams(params *ExecutionParams) error {
	if params.AWSConfig == nil {
		return fmt.Errorf("AWS configuration is required")
	}
	for _, envVar := range []string{"HYPERFLEET_DB_ENDPOINT", "HYPERFLEET_DB_NAME", "HYPERFLEET_DB_USERNAME"} {
		if os.Getenv(envVar) == "" {
			return fmt.Errorf("environment variable %s is not set", envVar)
		}
	}
	return nil
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
			{Name: "gvk", Required: true, Description: "Group/Version/Kind, e.g. hyperfleet.io/v1alpha1/Cluster (core group: /v1/ConfigMap). Run list_db_gvks to discover valid values."},
			{Name: "namespace", Description: "Target namespace (omit with all_namespaces, or for cluster-scoped resources)"},
			{Name: "all_namespaces", Default: "false", Description: "List across all namespaces (ignores namespace)"},
			{Name: "name", Description: "Get a specific resource by name"},
			{Name: "verbose", Default: "false", Description: "Return full objects (spec/status/metadata) instead of the summary table"},
		},
	}
}

func (a *getDBResource) Validate(_ context.Context, params *ExecutionParams) error {
	if err := validateDBConnParams(params); err != nil {
		return err
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
	verbose := params.Params["verbose"] == "true"

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
		"verbose", verbose,
	)

	pgRows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query resources: %w", err)
	}
	defer pgRows.Close()

	scanned := make([]dbResource, 0)
	for pgRows.Next() {
		var r dbResource
		if err := pgRows.Scan(
			&r.gvk, &r.namespace, &r.name, &r.uid, &r.objectVersion,
			&r.spec, &r.status, &r.metadata,
			&r.deletionTimestamp, &r.createdAt, &r.updatedAt,
			&r.ageSeconds,
		); err != nil {
			return nil, fmt.Errorf("failed to scan resource row: %w", err)
		}
		scanned = append(scanned, r)
	}
	if err := pgRows.Err(); err != nil {
		return nil, fmt.Errorf("failed reading resource rows: %w", err)
	}

	scope := namespace
	if allNamespaces || namespace == "" {
		scope = "all namespaces"
	}

	// A specific name lookup returns the single resource (or a not-found error),
	// mirroring `kubectl get <kind> <name>`.
	if name != "" {
		if len(scanned) == 0 {
			return nil, fmt.Errorf("%s %q not found in %s", gvk, name, scope)
		}
		if len(scanned) == 1 {
			var out interface{}
			if verbose {
				out = scanned[0].fullObject()
			} else {
				out = scanned[0].summaryRow()
			}
			return &ActionResult{
				Success: true,
				Output:  out,
				Summary: fmt.Sprintf("Retrieved %s %s", gvk, name),
			}, nil
		}
	}

	// Default output is a flat summary row per resource, which the CLI renders as
	// a kubectl-style table. Verbose returns the full objects for -o json / -v.
	var output interface{}
	if verbose {
		items := make([]map[string]interface{}, len(scanned))
		for i := range scanned {
			items[i] = scanned[i].fullObject()
		}
		output = items
	} else {
		items := make([]dbResourceRow, len(scanned))
		for i := range scanned {
			items[i] = scanned[i].summaryRow()
		}
		output = items
	}

	return &ActionResult{
		Success: true,
		Output:  output,
		Summary: fmt.Sprintf("Found %d %s in %s", len(scanned), gvk, scope),
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
		`deletion_timestamp::text, created_at::text, updated_at::text, ` +
		`EXTRACT(EPOCH FROM (now() - created_at))::bigint AS age_seconds ` +
		`FROM kubernetes_resources ` +
		`WHERE gvk = $1 ` +
		`AND ` + tombstoneFilter)
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
		return fmt.Errorf("invalid gvk %q: expected Group/Version/Kind (e.g. hyperfleet.io/v1alpha1/Cluster or /v1/ConfigMap)", gvk)
	}
	return nil
}

// dbResource holds one scanned row from kubernetes_resources. It is projected
// into either a summary row (default, table output) or a full object (verbose).
type dbResource struct {
	gvk, namespace, name, uid string
	objectVersion             int64
	spec, status, metadata    []byte
	deletionTimestamp         *string
	createdAt, updatedAt      *string
	ageSeconds                *int64
}

// dbResourceRow is the flat, scalar-only summary the CLI renders as a
// kubectl-style table. Struct field order (not map key order) sets the columns.
type dbResourceRow struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Age       string `json:"age"`
	State     string `json:"state"`
}

func (r dbResource) summaryRow() dbResourceRow {
	state := "Active"
	if r.deletionTimestamp != nil {
		state = "Terminating"
	}
	return dbResourceRow{
		Namespace: r.namespace,
		Name:      r.name,
		Version:   r.objectVersion,
		Age:       formatAge(r.ageSeconds),
		State:     state,
	}
}

func (r dbResource) fullObject() map[string]interface{} {
	return map[string]interface{}{
		"gvk":               r.gvk,
		"namespace":         r.namespace,
		"name":              r.name,
		"uid":               r.uid,
		"objectVersion":     r.objectVersion,
		"metadata":          decodeJSONB(r.metadata),
		"spec":              decodeJSONB(r.spec),
		"status":            decodeJSONB(r.status),
		"deletionTimestamp": r.deletionTimestamp,
		"createdAt":         r.createdAt,
		"updatedAt":         r.updatedAt,
	}
}

// formatAge renders an age in seconds as a compact kubectl-style string
// (e.g. 45s, 12m, 5h, 9d). Returns "-" when the age is unknown.
func formatAge(seconds *int64) string {
	if seconds == nil {
		return "-"
	}
	s := *seconds
	if s < 0 {
		s = 0
	}
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	default:
		return fmt.Sprintf("%dd", s/86400)
	}
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
