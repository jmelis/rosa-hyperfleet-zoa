package actions

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

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

func newDBParams(gvk string) *ExecutionParams {
	return &ExecutionParams{
		AWSConfig: &aws.Config{Region: "us-east-1"},
		Params:    map[string]string{"gvk": gvk},
	}
}

func TestGetDBResourceValidate_WhenAWSConfigMissing_ItShouldError(t *testing.T) {
	err := (&getDBResource{}).Validate(context.Background(), &ExecutionParams{Params: map[string]string{"gvk": "apps/v1/Deployment"}})
	if err == nil {
		t.Fatal("expected error when AWSConfig is nil")
	}
}

func TestGetDBResourceValidate_WhenEnvVarMissing_ItShouldError(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	os.Unsetenv("HYPERFLEET_DB_USERNAME")

	err := (&getDBResource{}).Validate(context.Background(), newDBParams("apps/v1/Deployment"))
	if err == nil {
		t.Fatal("expected error when HYPERFLEET_DB_USERNAME is unset")
	}
}

func TestGetDBResourceValidate_WhenGVKMissing_ItShouldError(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	t.Setenv("HYPERFLEET_DB_USERNAME", "zoa_ro")

	params := &ExecutionParams{AWSConfig: &aws.Config{Region: "us-east-1"}, Params: map[string]string{}}
	if err := (&getDBResource{}).Validate(context.Background(), params); err == nil {
		t.Fatal("expected error when gvk is missing")
	}
}

func TestGetDBResourceValidate_WhenGVKMalformed_ItShouldError(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	t.Setenv("HYPERFLEET_DB_USERNAME", "zoa_ro")

	for _, bad := range []string{"Deployment", "apps/Deployment", "apps//Deployment", "apps/v1/"} {
		if err := (&getDBResource{}).Validate(context.Background(), newDBParams(bad)); err == nil {
			t.Errorf("expected error for malformed gvk %q", bad)
		}
	}
}

func TestGetDBResourceValidate_WhenValid_ItShouldPass(t *testing.T) {
	t.Setenv("HYPERFLEET_DB_ENDPOINT", "host:5432")
	t.Setenv("HYPERFLEET_DB_NAME", "hyperfleet")
	t.Setenv("HYPERFLEET_DB_USERNAME", "zoa_ro")

	for _, good := range []string{"apps/v1/Deployment", "hypershift.openshift.io/v1beta1/HostedCluster", "/v1/ConfigMap"} {
		if err := (&getDBResource{}).Validate(context.Background(), newDBParams(good)); err != nil {
			t.Errorf("unexpected error for valid gvk %q: %v", good, err)
		}
	}
}

func TestBuildListQuery_WhenListingAllNamespaces_ItShouldFilterByGVKOnly(t *testing.T) {
	q, args := buildListQuery("apps/v1/Deployment", "kube-system", "", true)

	if len(args) != 1 || args[0] != "apps/v1/Deployment" {
		t.Fatalf("expected only gvk arg, got %v", args)
	}
	// all_namespaces must ignore the provided namespace
	if strings.Contains(q, "namespace = $") {
		t.Errorf("expected no namespace filter with all_namespaces, got: %s", q)
	}
	if !strings.Contains(q, "finalizers") {
		t.Errorf("expected tombstone filter in query, got: %s", q)
	}
}

func TestBuildListQuery_WhenNamespaceGiven_ItShouldFilterByNamespace(t *testing.T) {
	q, args := buildListQuery("apps/v1/Deployment", "team-a", "", false)

	if len(args) != 2 || args[1] != "team-a" {
		t.Fatalf("expected gvk + namespace args, got %v", args)
	}
	if !strings.Contains(q, "AND namespace = $2") {
		t.Errorf("expected namespace filter $2, got: %s", q)
	}
}

func TestBuildListQuery_WhenNameGiven_ItShouldFilterByName(t *testing.T) {
	q, args := buildListQuery("apps/v1/Deployment", "team-a", "web", false)

	if len(args) != 3 || args[1] != "team-a" || args[2] != "web" {
		t.Fatalf("expected gvk + namespace + name args, got %v", args)
	}
	if !strings.Contains(q, "AND namespace = $2") || !strings.Contains(q, "AND name = $3") {
		t.Errorf("expected namespace $2 and name $3 filters, got: %s", q)
	}
}

func TestBuildListQuery_WhenNameWithoutNamespace_ItShouldFilterByNameOnly(t *testing.T) {
	q, args := buildListQuery("hypershift.openshift.io/v1beta1/HostedCluster", "", "my-hc", false)

	if len(args) != 2 || args[1] != "my-hc" {
		t.Fatalf("expected gvk + name args, got %v", args)
	}
	if strings.Contains(q, "namespace = $") {
		t.Errorf("expected no namespace filter, got: %s", q)
	}
	if !strings.Contains(q, "AND name = $2") {
		t.Errorf("expected name filter $2, got: %s", q)
	}
}

func TestSummaryRow_WhenLive_ItShouldBeFlatAndActive(t *testing.T) {
	age := int64(3 * 86400)
	r := dbResource{namespace: "clusters", name: "web", objectVersion: 7, ageSeconds: &age}

	row := r.summaryRow()
	if row.Namespace != "clusters" || row.Name != "web" || row.Version != 7 {
		t.Errorf("unexpected identity fields: %+v", row)
	}
	if row.Age != "3d" {
		t.Errorf("expected age 3d, got %q", row.Age)
	}
	if row.State != "Active" {
		t.Errorf("expected state Active, got %q", row.State)
	}
}

func TestSummaryRow_WhenDeletionTimestampSet_ItShouldBeTerminating(t *testing.T) {
	ts := "2026-09-29 00:00:00+00"
	r := dbResource{namespace: "clusters", name: "web", deletionTimestamp: &ts}

	if got := r.summaryRow().State; got != "Terminating" {
		t.Errorf("expected state Terminating, got %q", got)
	}
}

// TestDBResourceRow_JSONFieldOrder pins the serialized field order of the
// summary row. The CLI renders a JSON array of flat objects as a table whose
// columns follow serialization order, so this order IS the column order
// (NAMESPACE, NAME, VERSION, AGE, STATE). Using a struct (not a map) is what
// makes it stable; this test fails loudly if the fields are ever reordered.
func TestDBResourceRow_JSONFieldOrder_ItShouldControlColumnOrder(t *testing.T) {
	b, err := json.Marshal(dbResourceRow{Namespace: "ns", Name: "n", Version: 1, Age: "5m", State: "Active"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"namespace":"ns","name":"n","version":1,"age":"5m","state":"Active"}`
	if got := string(b); got != want {
		t.Errorf("summary row field order drifted (breaks table columns):\n got: %s\nwant: %s", got, want)
	}
}

// TestFullObject checks the verbose projection: identity scalars carried
// verbatim, JSONB columns decoded into nested structures (not raw strings), and
// timestamps passed through as scanned.
func TestFullObject_ItShouldNestDecodedJSONBAndCarryIdentity(t *testing.T) {
	ts := "2026-09-29 00:00:00+00"
	r := dbResource{
		gvk:               "hyperfleet.io/v1alpha1/Cluster",
		namespace:         "cluster-abc",
		name:              "web",
		uid:               "uid-123",
		objectVersion:     42,
		spec:              []byte(`{"replicas":3}`),
		status:            []byte(`{"phase":"Ready"}`),
		metadata:          []byte(`{"labels":{"team":"a"}}`),
		deletionTimestamp: &ts,
		createdAt:         &ts,
		updatedAt:         &ts,
	}

	obj := r.fullObject()

	if obj["gvk"] != "hyperfleet.io/v1alpha1/Cluster" || obj["namespace"] != "cluster-abc" ||
		obj["name"] != "web" || obj["uid"] != "uid-123" || obj["objectVersion"] != int64(42) {
		t.Errorf("unexpected identity fields: %+v", obj)
	}

	spec, ok := obj["spec"].(map[string]interface{})
	if !ok || spec["replicas"] != float64(3) {
		t.Errorf("expected spec decoded to map with replicas=3, got %T %v", obj["spec"], obj["spec"])
	}
	status, ok := obj["status"].(map[string]interface{})
	if !ok || status["phase"] != "Ready" {
		t.Errorf("expected status decoded to map with phase=Ready, got %v", obj["status"])
	}
	if _, ok := obj["metadata"].(map[string]interface{}); !ok {
		t.Errorf("expected metadata decoded to map, got %T", obj["metadata"])
	}

	if got, _ := obj["deletionTimestamp"].(*string); got == nil || *got != ts {
		t.Errorf("expected deletionTimestamp %q passed through, got %v", ts, obj["deletionTimestamp"])
	}
}

func TestFormatAge(t *testing.T) {
	sec := func(n int64) *int64 { return &n }
	cases := map[string]struct {
		in   *int64
		want string
	}{
		"nil":                 {nil, "-"},
		"seconds":             {sec(45), "45s"},
		"minutes":             {sec(12 * 60), "12m"},
		"hours":               {sec(5 * 3600), "5h"},
		"days":                {sec(9 * 86400), "9d"},
		"negative clock skew": {sec(-10), "0s"},
	}
	for name, tc := range cases {
		if got := formatAge(tc.in); got != tc.want {
			t.Errorf("%s: formatAge = %q, want %q", name, got, tc.want)
		}
	}
}

func TestDecodeJSONB_WhenValidObject_ItShouldReturnMap(t *testing.T) {
	v := decodeJSONB([]byte(`{"replicas":3}`))
	m, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", v)
	}
	if m["replicas"] != float64(3) {
		t.Errorf("expected replicas=3, got %v", m["replicas"])
	}
}

func TestDecodeJSONB_WhenEmpty_ItShouldReturnNil(t *testing.T) {
	if v := decodeJSONB(nil); v != nil {
		t.Errorf("expected nil for empty input, got %v", v)
	}
}
