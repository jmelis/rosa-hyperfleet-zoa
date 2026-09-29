# CLI Reference

The `zoa` CLI communicates with the Lambda API via SigV4-signed HTTPS requests.

## Design Philosophy

The CLI is deliberately built around **SRE muscle memory** — the patterns and conventions that operators have internalized from years of daily use of `kubectl`, `aws-cli`, and `oc`. This means:

- **Familiar flags**: `-n` for namespace, `-o json` for machine output, `-A` for all-namespaces, `-v` for verbose — no learning curve
- **Composable output**: table by default (human), JSON for piping into `jq`, same pattern as `kubectl get -o json | jq`
- **Subcommand structure**: verb-first (`run`, `get`, `describe`, `logs`) mirrors kubectl's mental model
- **Predictable behavior**: `--dry-run` previews, `--force` overrides safety, `--wait` blocks until done — exactly what you'd expect
- **Shell completion**: full zsh/bash/fish completion so discoverability is instant
- **Zero configuration**: inherit AWS credentials from the environment (same as aws-cli), single env var for endpoint

The goal: an SRE who has never seen ZOA should be productive within 30 seconds of reading `zoa --help`.

## Install

Download a pinned GitHub Release binary and verify the checksum.

Linux x86_64:

```bash
VERSION=v0.3.0  # see https://github.com/openshift-online/rosa-hyperfleet-zoa/releases
curl -fsSL -O "https://github.com/openshift-online/rosa-hyperfleet-zoa/releases/download/${VERSION}/zoa-linux-amd64"
curl -fsSL -O "https://github.com/openshift-online/rosa-hyperfleet-zoa/releases/download/${VERSION}/SHA256SUMS"
sha256sum -c SHA256SUMS --ignore-missing
chmod +x zoa-linux-amd64
sudo mv zoa-linux-amd64 /usr/local/bin/zoa
zoa version
```

Other platforms — replace the binary name in the commands above:

| OS / Arch | Binary |
|-----------|--------|
| Linux x86_64 | `zoa-linux-amd64` |
| Linux ARM64 | `zoa-linux-arm64` |
| macOS Intel | `zoa-darwin-amd64` |
| macOS Apple Silicon | `zoa-darwin-arm64` |

On macOS, `sha256sum` is not installed by default. Use the built-in equivalent:

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
```

### Development builds

If you work on this repo, use `make build` (or `make install`). These inject
full version metadata via ldflags, so `zoa version` reports the exact commit
and build date — useful for debugging which build is deployed.

## Configuration

```bash
export ZOA_API_URL="https://<id>.lambda-url.<region>.on.aws"
```

AWS credentials must be available in the environment (e.g., via `aws configure export-credentials`).

## Commands

| Command | Description |
|---------|-------------|
| `run <action>` | Execute a Trusted Action |
| `runs` | List recent executions |
| `get <id>` | Get execution details |
| `output <id>` | Show execution output |
| `logs <id>` | Show execution logs |
| `download <id>` | Download output to a local file |
| `actions` | List all available Trusted Actions |
| `describe <action>` | Show TA details (params, scope, timeout) |
| `audit` | View audit trail |
| `version` | Print client and server version |
| `completion` | Generate shell completion scripts |

## Examples

```bash
# Discover
zoa actions
zoa describe get_resource

# Read resources (sync, default)
zoa run get_resource --jira OSD-123 --namespace kube-system --resource pods
zoa run get_resource --jira OSD-123 --resource nodes
zoa run get_resource --jira OSD-123 --namespace cert-manager --resource deployments

# Read with verbose output (full API objects)
zoa run get_resource --jira OSD-123 --resource pods -A --verbose

# AWS API reads
zoa run list_eks_clusters --jira OSD-123
zoa run describe_eks_cluster --jira OSD-123 --name my-cluster

# Write actions (have cooldown)
zoa run delete_pod --jira OSD-123 --namespace grafana --name grafana-abc123
zoa run rollout_restart --jira OSD-123 --namespace cert-manager --resource deployment --name cert-manager

# Dry-run a write action (executes the read preview)
zoa run delete_pod --jira OSD-123 --namespace grafana --name grafana-abc123 --dry-run

# Force (bypass cooldown)
zoa run delete_pod --jira OSD-123 --namespace grafana --name grafana-abc123 --force

# Async execution (for longer-running operations)
zoa run get_resource --jira OSD-123 --namespace kube-system --resource pods --async
zoa run get_resource --jira OSD-123 --namespace kube-system --resource pods --async --wait

# View results
zoa runs --limit 10
zoa runs -o wide              # Full details: dispatched/completed timestamps, log bytes
zoa runs --since 7d           # Last 7 days
zoa runs --since 2026-08-20 --until 2026-08-25  # Date range
zoa get <exec-id> --include-output
zoa output <exec-id>
zoa logs <exec-id>
zoa download <exec-id>
zoa download <exec-id> -f /tmp/output.json

# JSON output (for piping)
zoa runs -o json
zoa get <exec-id> -o json

# Audit
zoa audit --limit 20
zoa audit --since 7d --target my-cluster
zoa audit --since 2026-08-01 --until 2026-08-15
```

## HyperFleet DB actions

`get_db_resource` and `list_db_gvks` read HyperFleet control-plane resources directly from the hyperfleet-db (Aurora PostgreSQL), bypassing the Kubernetes API. They run on the RC ZOA and authenticate to the database with an RDS IAM token. Resources are keyed by GVK (`Group/Version/Kind`); the core group has an empty group, so its GVKs start with a leading slash (e.g. `/v1/ConfigMap`).

```bash
# Discover which resource types exist (with object counts). Run this first.
zoa run list_db_gvks --jira ROSAENG-1234

# List all objects of a type across every namespace
zoa run get_db_resource --jira ROSAENG-1234 --param gvk=hypershift.openshift.io/v1beta1/HostedCluster -A

# List a type within a namespace
zoa run get_db_resource --jira ROSAENG-1234 --param gvk=apps/v1/Deployment -n clusters-mycluster

# Get a namespaced resource by name (returns the single object)
zoa run get_db_resource --jira ROSAENG-1234 --param gvk=hypershift.openshift.io/v1beta1/HostedCluster -n clusters --name my-hosted-cluster

# Get a cluster-scoped resource by name (no namespace)
zoa run get_db_resource --jira ROSAENG-1234 --param gvk=hypershift.openshift.io/v1beta1/ManagementCluster --name mc-01

# Core-group resource (leading slash for the empty group)
zoa run get_db_resource --jira ROSAENG-1234 --param gvk=/v1/ConfigMap -n kube-system

# Full JSON objects (spec/status/metadata) instead of the compact summary
zoa run get_db_resource --jira ROSAENG-1234 --param gvk=apps/v1/Deployment -n clusters-mycluster -v
```

**Notes:**

- Run `list_db_gvks` first. A Kind that is not present in the DB returns an empty result rather than an error, so it doubles as a way to confirm the exact GVK string.
- With no `--name`, the action lists; with `--name`, it returns the single matching object or a not-found error (mirrors `kubectl get <kind> <name>`).
- Omitting `-n`/`-A` lists across all namespaces. Most HyperFleet resources are cluster-scoped (stored with an empty namespace), so this is usually what you want.
- Fully-deleted objects are always excluded; objects still holding finalizers (mid-deletion) are shown, matching the operator's own view.

## Global Flags

| Flag | Short | Env Var | Description |
|------|-------|---------|-------------|
| `--api-url` | | `ZOA_API_URL` | ZOA endpoint URL (Function URL, API Gateway, or CNAME) |
| `--output` | `-o` | | Output format: `table` (default), `wide`, `json` |
| `--region` | | `AWS_REGION` | AWS region override for custom CNAME endpoints |
| `--help` | `-h` | | Help for any command |

## `run` Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--jira` | | Jira ticket (required, e.g. ROSAENG-1234) |
| `--namespace` | `-n` | Target namespace |
| `--all-namespaces` | `-A` | All namespaces |
| `--name` | | Resource name |
| `--resource` | | Resource type (for generic actions) |
| `--selector` | `-l` | Label selector |
| `--verbose` | `-v` | Full JSON output (no compact summary) |
| `--param` | | Additional parameters (key=value, repeatable) |
| `--force` | | Bypass write cooldown and concurrency limits |
| `--dry-run` | | Execute the dry-run variant of the action |
| `--execution-mode` | | Override execution class: `sync` or `async` (default: TA's declared class) |
| `--timeout` | | Server-side TA execution timeout (e.g. 60s, 3m; bounded by server max 295s) |
| `--no-wait` | | Return ID immediately, skip output display |
| `--wait` | | Poll until async execution completes (no effect on sync — sync returns inline) |
| `--wait-timeout` | | Max poll duration when `--wait` is active (default 5m) |
| `--wait-poll-interval` | | Poll frequency when `--wait` is active (default 30s) |
| `--gather` | | `must_gather` scopes: `hcp`, `mc`, `rc` (comma-separated; must match the ZOA endpoint) |
| `--cluster-id` | | Hosted cluster UUID (`must_gather` when `--gather` includes `hcp`) |

### `must_gather`

**Must-gather** is the standard troubleshooting bundle: Kubernetes logs, events, and resource manifests packaged as `output.tar.gz` for offline analysis (compatible with `omc`/`omg`-style layouts).

| `--gather` | Collects |
|------------|----------|
| `hcp` | One hosted ROSA cluster: hypershift dump + HCP/control-plane namespace diagnostics (MC ZOA only; requires `--cluster-id`) |
| `mc` | Management cluster platform: platform namespaces (e.g. kube-applier, hypershift), nodes, storage, Karpenter CRs |
| `rc` | Regional cluster platform: platform namespaces (e.g. platform-api), HyperFleet CRs, nodes, storage, Karpenter CRs |

Read-only. Produces `output.tar.gz` (`zoa download`). Mode is async (see MODE column); use `--wait` to block.

```bash
# MC platform
zoa run must_gather --jira OSD-123 --gather mc --wait

# RC platform
zoa run must_gather --jira OSD-123 --gather rc --wait

# Hosted cluster (MC ZOA only)
zoa run must_gather --jira OSD-123 --gather hcp --cluster-id <uuid> --wait

zoa describe must_gather   # full parameter reference
zoa download <exec-id> -f /tmp/must-gather.tar.gz
```

Use `zoa describe must_gather` for parameters such as `extra_namespaces` and `skip_must_gather_image`. Other actions use shared flags above (`--namespace`, `--resource`, …) only.

## `runs` Filters

All filters are combinable:

| Flag | Short | Description |
|------|-------|-------------|
| `--status` | | Filter by status (dispatched, succeeded, failed, timed_out) |
| `--action` | | Filter by action name |
| `--operator` | | Filter by operator |
| `--target` | `-t` | Filter by target cluster |
| `--scope` | | Filter by scope (kube-api, aws-api) |
| `--type` | | Filter by type (read, write) |
| `--execution-mode` | | Filter by execution class (sync, async) |
| `--since` | | Show entries after this point (default: `24h`). See [Time Formats](#time-formats). |
| `--until` | | Show entries before this point. See [Time Formats](#time-formats). |
| `--dry-run` | | Show only dry-run executions |
| `--limit` | | Max results (max 100, default 20) |

## `get` Flags

| Flag | Description |
|------|-------------|
| `--include-output` | Include execution output in display |
| `--include-logs` | Include execution logs |
| `--include-all` | Include both output and logs |
| `--wait` | Poll until execution reaches terminal status (useful to reconnect) |
| `--wait-timeout` | Max poll duration when `--wait` is active (default 5m) |
| `--wait-poll-interval` | Poll frequency when `--wait` is active (default 30s) |

## `audit` Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--action` | | Filter by action name |
| `--operator` | | Filter by operator |
| `--target` | `-t` | Filter by target cluster |
| `--method` | | Filter by HTTP method (GET, POST) |
| `--approval` | | Filter by approval state |
| `--since` | | Show entries after this point (default: `24h`). See [Time Formats](#time-formats). |
| `--until` | | Show entries before this point. See [Time Formats](#time-formats). |
| `--limit` | | Max results (max 200, default 50) |

## `download` Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--file` | `-f` | Destination file path (default: `zoa-<id>-<artifact>.json`) |
| `--artifact` | | Which artifact to download: `output` (default), `logs` |

## Time Formats

Both `--since` and `--until` accept the same flexible formats:

| Format | Example | Meaning |
|--------|---------|---------|
| Duration | `1h`, `24h`, `7d`, `30m`, `300s` | Relative to now (subtracted) |
| Short date | `2026-08-25` | Start-of-day for `--since`, end-of-day for `--until` |
| RFC3339 | `2026-08-25T14:30:00Z` | Exact timestamp |
| RFC3339 with offset | `2026-08-25T16:30:00+02:00` | Exact timestamp (converted to UTC) |

**Behavior notes:**

- `--since` defaults to `24h` if not specified (shows last 24 hours)
- `--until` is optional; when omitted, results go up to the present
- Short dates are interpreted inclusively: `--since 2026-08-25` means "from the start of Aug 25" and `--until 2026-08-25` means "through the end of Aug 25"
- Results are always sorted newest-first

**Examples:**

```bash
# Last 24 hours (default)
zoa runs

# Last 7 days
zoa runs --since 7d

# Specific day range (inclusive)
zoa runs --since 2026-08-20 --until 2026-08-25

# Since a specific timestamp
zoa runs --since 2026-08-25T09:00:00Z

# Time window: between 2 hours ago and 30 minutes ago
zoa runs --since 2h --until 30m

# Same filters work on audit
zoa audit --since 7d --target my-cluster
zoa audit --since 2026-08-01 --until 2026-08-15
```

## Shell Completion

```bash
source <(zoa completion zsh)     # zsh
source <(zoa completion bash)    # bash
zoa completion fish | source     # fish
```
