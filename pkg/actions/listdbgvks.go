package actions

import (
	"context"
	"fmt"
)

func init() {
	Register(&listDBGVKs{})
}

type listDBGVKs struct{}

// listGVKsQuery returns each distinct GVK present in the table with a count of
// its live/dying objects (tombstones excluded), ordered for stable output.
const listGVKsQuery = `SELECT gvk, count(*) AS count ` +
	`FROM kubernetes_resources ` +
	`WHERE ` + tombstoneFilter + ` ` +
	`GROUP BY gvk ` +
	`ORDER BY gvk`

func (a *listDBGVKs) Metadata() ActionMetadata {
	return ActionMetadata{
		Name:          "list_db_gvks",
		Scope:         "aws-api",
		Type:          "read",
		ExecutionMode: "sync",
		Description:   "List the distinct resource types (GVKs) present in the hyperfleet-db, with object counts. Use to discover valid gvk values for get_db_resource.",
		Authorization: AuthorizationConfig{Approval: "none"},
		// RC only: hyperfleet-db lives in the regional cluster VPC.
		DeploymentTargets: []string{DeploymentTargetRC},
		TimeoutSeconds:    30,
		Parameters:        []ParameterDef{},
	}
}

func (a *listDBGVKs) Validate(_ context.Context, params *ExecutionParams) error {
	return validateDBConnParams(params)
}

func (a *listDBGVKs) Execute(ctx context.Context, params *ExecutionParams) (*ActionResult, error) {
	conn, err := connectHyperfleetDB(ctx, params)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	params.Logger.Info("listing hyperfleet-db GVKs")

	rows, err := conn.Query(ctx, listGVKsQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query GVKs: %w", err)
	}
	defer rows.Close()

	items := make([]map[string]interface{}, 0)
	for rows.Next() {
		var (
			gvk   string
			count int64
		)
		if err := rows.Scan(&gvk, &count); err != nil {
			return nil, fmt.Errorf("failed to scan GVK row: %w", err)
		}
		items = append(items, map[string]interface{}{
			"gvk":   gvk,
			"count": count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed reading GVK rows: %w", err)
	}

	return &ActionResult{
		Success: true,
		Output:  items,
		Summary: fmt.Sprintf("Found %d resource types", len(items)),
	}, nil
}
