# End-to-End Testing

Practical guide for ZOA's e2e coverage: what runs where, how to run it yourself against a real
environment (dev-account ephemeral, standing integration, or CI), and how container images flow
from a PR into that environment.

## Test Suites

ZOA has two independent test suites that compose via Makefile targets:

| Suite          | Path                    | What it validates                                                      | Requires                     |
| -------------- | ----------------------- | ---------------------------------------------------------------------- | ---------------------------- |
| **Functional** | `test/e2e/`             | ZOA CLI, Trusted Actions, executor, RBAC, cooldown, audit, CLI output  | `ZOA_RC_API_URL` and/or `ZOA_MC_API_URL` |
| **Monitoring** | `test/e2e-monitoring/`  | Observability pipeline — metrics in Thanos, recording rules, alerts    | `RHOBS_API_URL` (required) |

Both suites use **Ginkgo labels** to define scope:

- **Smoke** (`Label("smoke")`): minimal subset that validates core health in ~2 min.
  Runs on every PR via consumer repos (`rosa-hyperfleet`, `rosa-hyperfleet-api`).
- **Full** (all specs): comprehensive coverage. Runs on nightlies and ZOA's own `on-demand-e2e`.

### Makefile Targets

| Target                     | What runs                                       |
| -------------------------- | ----------------------------------------------- |
| `make test-e2e`            | Functional full → Monitoring full               |
| `make test-e2e-smoke`      | Functional smoke → Monitoring smoke             |
| `make test-e2e-zoa`        | Functional full only                            |
| `make test-e2e-zoa-smoke`  | Functional smoke only                           |
| `make test-e2e-monitoring` | Monitoring full only (requires `RHOBS_API_URL`) |

The default targets (`test-e2e`, `test-e2e-smoke`) chain both suites. `RHOBS_API_URL` is
required — missing it fails the suite. Use `test-e2e-zoa` / `test-e2e-zoa-smoke` to run
functional tests only.

### Functional Suite Details

**Smoke** (6 specs, ~2 min per target): `zoa version`, `zoa actions` discovery, one kube-api
read (`get_resource --resource nodes`), one aws-api read (`list_eks_clusters`), one write TA
`--dry-run` (`rollout_restart`), one async execution with `--wait`.

**Full**: every registered Trusted Action, both scopes (`kube-api`/`aws-api`),
read+write+dry-run, sync+async, RC and MC, real (non-dry-run) `delete_pod`/`rollout_restart`
against `kube-system/coredns`, cooldown enforcement, all CLI commands (`get`, `runs`, `audit`,
`output`, `logs`, `download`), generic param validation, e2e conformance checks.

### Monitoring Suite Details

**Smoke**: recording rules loaded in Thanos Ruler, alerting rules loaded, infrastructure
metrics present (Lambda invocations, reconciler ticks). These are always-on metrics that do
not depend on TA executions.

**Full**: all smoke checks plus execution metrics with expected dimensions (Status, Mode,
Scope, Type), recording rule values populated, no critical alerts in `firing` state.

See [docs/observability.md](observability.md) for the metrics catalog and alerting philosophy.

### Metric Lag

The CloudWatch pipeline latency is 5–7 minutes. Monitoring tests run after functional tests
and use `Eventually("5m", "15s")`, so the total window from first TA execution to metric
assertion is sufficient. Infrastructure metrics (Lambda invocations, reconciler ticks) are
always-on and do not depend on test-driven TA execution.

## Developer Workflow: Testing Code Changes

When you change ZOA code (API, Trusted Actions, executor, etc.) and want to validate against a
live ephemeral environment, you must **build and deploy your own images first**. The e2e test
suite only exercises the `zoa` CLI — the Lambda running on the ephemeral must already contain
your code changes.

> **Important:** Automatic image injection (CI builds your PR's images and deploys them) only
> happens in CI via the `on-demand-e2e` Prow job. For local development, you manage the image
> lifecycle manually.

### Image Naming Reference

| Source | Image Tag Format | Registry |
|--------|------------------|----------|
| **PR builds** (Tekton) | `on-pr-<commit-sha>` | `quay.io/redhat-user-workloads/rosa-tenant/zoa-lambda` |
| **Main branch** (Tekton) | `<commit-sha>` | `quay.io/redhat-user-workloads/rosa-tenant/zoa-lambda` |
| **Dev builds** (local) | `latest` or `<commit-sha>` | `quay.io/rrp-dev-ci/zoa-lambda` |
| **Manual** | Any tag you choose | Your quay.io namespace |

PR images expire after 5 days. Main-branch and dev images persist indefinitely.

### Step-by-step (Using PR Images)

The recommended approach is to push your changes to a branch and let Tekton build the images automatically:

```bash
# 1. Push your code to trigger PR image build
cd rosa-hyperfleet-zoa
git push origin HEAD:feat/my-feature   # or push to main

# 2. Open a PR on GitHub in normal mode (NOT draft) with hold label
# Use --label to prevent accidental merges during testing
gh pr create \
  --title "feat: your feature description" \
  --body "Description of changes..." \
  --label "do-not-merge/hold" \
  --base main

# 3. Trigger the Konflux build (required when pushing from a fork)
# Add this comment to the PR to start the build
gh pr comment <pr-number> --body "/ok-to-test"

# 4. Wait for the build check to pass in the PR
# Tekton will build: quay.io/redhat-user-workloads/rosa-tenant/zoa-lambda:on-pr-<commit>

# 5. Get your commit SHA
git rev-parse --short HEAD   # e.g. 1fa64b3

# 6. Configure ephemeral override in rosa-hyperfleet
cd ../rosa-hyperfleet
mkdir -p .ephemeral-env

# Create override config pointing to your PR images
cat > .ephemeral-env/defaults.yaml <<EOF
regional_cluster:
  zoa_lambda_image_tag: "on-pr-1fa64b3"
  zoa_runner_image_tag: "on-pr-1fa64b3"
  zoa_lambda_source_image: "quay.io/redhat-user-workloads/rosa-tenant/zoa-lambda"
  zoa_runner_source_image: "quay.io/redhat-user-workloads/rosa-tenant/zoa-runner"
EOF

# Create region file (required for .ephemeral-env/)
cat > .ephemeral-env/us-east-1.yaml <<EOF
provision_mcs:
  mc01: {}
EOF

# 7. Resync the ephemeral to deploy the new Lambda
make ephemeral-resync ID=<your-env-id>

# 8. Run e2e tests (picks up your code changes via the new Lambda)
make ephemeral-zoa-e2e ID=<your-env-id> \
  ZOA_REF=feat/my-feature \
  ZOA_REPO=https://github.com/openshift-online/rosa-hyperfleet-zoa.git
```

### Alternative: Manual Image Build and Push

If you need to test uncommitted changes or don't have PR access, you can build and push manually:

```bash
# 1. Build + push both images to Quay (requires quay.io login and write access)
cd rosa-hyperfleet-zoa
podman login quay.io
make images-push IMAGE_REPO=quay.io/your-username/zoa-lambda \
                RUNNER_IMAGE_REPO=quay.io/your-username/zoa-runner \
                IMAGE_TAG=test-my-feature

# 2. Configure ephemeral override to use your images
cd ../rosa-hyperfleet
mkdir -p .ephemeral-env

cat > .ephemeral-env/defaults.yaml <<EOF
regional_cluster:
  zoa_lambda_image_tag: "test-my-feature"
  zoa_runner_image_tag: "test-my-feature"
  zoa_lambda_source_image: "quay.io/your-username/zoa-lambda"
  zoa_runner_source_image: "quay.io/your-username/zoa-runner"
EOF

cat > .ephemeral-env/us-east-1.yaml <<EOF
provision_mcs:
  mc01: {}
EOF

# 3. Resync and test
make ephemeral-resync ID=<your-env-id>
make ephemeral-zoa-e2e ID=<your-env-id>
```

If you are only changing **test code** (files in `test/e2e/` or `test/e2e-monitoring/`) and
not API/TA code, skip the image build steps entirely — the existing Lambda is fine, you just
need the tests to use your branch (via `ZOA_REF`).

**Notes:**
- The `.ephemeral-env/` directory is gitignored and only affects your local machine
- PR images expire after 5 days (sufficient for testing)
- **Do not create draft PRs** - Konflux builds may not trigger for drafts; use `do-not-merge/hold` label instead
- When pushing from a fork, you must add `/ok-to-test` comment to trigger builds
- If you need to iterate quickly on uncommitted changes, use the manual build approach
- Remember to remove or update `.ephemeral-env/defaults.yaml` after testing to avoid using stale images
- Remove the `do-not-merge/hold` label when ready to merge

### When is image management automatic?

| Scenario | Image management |
| --- | --- |
| **CI `on-demand-e2e`** (ZOA PR) | Automatic — ci-operator builds images, pushes to Quay, and overrides config before deploy |
| **CI `nightly-ephemeral`** | None — tests whatever tags are already pinned in `rosa-hyperfleet@main` config |
| **Local dev testing** | **Manual** — you build, push, configure, and resync (steps above) |

## Running the Tests

Both suites drive against a real, already-provisioned environment. They never need a specific
environment shape beyond "give me a URL" — the same suites run unmodified against a dev-account
ephemeral env, a CI ephemeral env, or (in principle) a standing integration environment.

### From `rosa-hyperfleet` (Recommended)

You need an ephemeral environment already provisioned (`make ephemeral-provision` in that repo —
see [`rosa-hyperfleet/docs/development-environment.md`](https://github.com/openshift-online/rosa-hyperfleet/blob/main/docs/development-environment.md)).

```bash
cd rosa-hyperfleet

# Full suite (functional + monitoring) — clones rosa-hyperfleet-zoa@main inside the test container.
# AWS credentials (rrp-rc, rrp-mc profiles) and RHOBS_API_URL are set up automatically.
make ephemeral-zoa-e2e \
  ID=<your-env-id> \
  ZOA_REF=my-feature-branch \
  ZOA_REPO=https://github.com/my-fork/rosa-hyperfleet-zoa.git

# Smoke only (~2min)
make ephemeral-zoa-e2e-smoke ID=<your-env-id>

# Verbose output
GINKGO_FLAGS=-ginkgo.v make ephemeral-zoa-e2e ID=<your-env-id>

# Only functional tests (skip monitoring)
ZOA_MAKE_TARGET=test-e2e-zoa make ephemeral-zoa-e2e ID=<your-env-id>

# Only monitoring tests
ZOA_MAKE_TARGET=test-e2e-monitoring make ephemeral-zoa-e2e ID=<your-env-id>
```

`ZOA_REF` defaults to `main`, `ZOA_REPO` defaults to the upstream repo. To test uncommitted
changes, push them to a branch first — there is no local-checkout-mount option (same convention
as `E2E_REF`/`E2E_REPO` for API tests and `CLI_REF`/`CLI_REPO` for CLI tests).

This is the recommended path because:

- AWS credentials are handled automatically (the container maps your host profiles to
  `rrp-rc`/`rrp-mc` inside the container).
- ZOA Lambda URLs and RHOBS API URL are resolved from Terraform outputs.
- No manual env var setup needed.

### From This Repo Directly

If you are iterating on test code and want fast feedback without container overhead,
you can run the suite directly against an existing ephemeral environment. You need the
Lambda Function URLs and AWS credentials for both the RC and MC accounts.

**Step 1 — Get the Lambda URLs.** From the `rosa-hyperfleet` checkout:

```bash
make ephemeral-list ID=<your-env-id>
# Look for the ZOA RC and MC API URLs and RHOBS API URL in the output
```

**Step 2 — Run both RC and MC together.** Your host `~/.aws/config` has profiles named
`rrp-regional-dev` and `rrp-management-dev`, but the test suite defaults to `rrp-rc` and `rrp-mc`
(the names used inside CI containers). Override with `ZOA_RC_AWS_PROFILE` and `ZOA_MC_AWS_PROFILE`:

```bash
cd rosa-hyperfleet-zoa

export ZOA_RC_API_URL="https://yyyy.lambda-url.us-east-1.on.aws/"
export ZOA_MC_API_URL="https://zzzz.lambda-url.us-east-1.on.aws/"
export ZOA_RC_AWS_PROFILE="rrp-regional-dev"
export ZOA_MC_AWS_PROFILE="rrp-management-dev"
export RHOBS_API_URL="https://xxxxx.execute-api.us-east-1.amazonaws.com/prod"

make test-e2e              # full: functional + monitoring
make test-e2e-smoke        # smoke: functional + monitoring
make test-e2e-zoa          # full: functional only
make test-e2e-monitoring   # full: monitoring only
GINKGO_FLAGS=-ginkgo.v make test-e2e   # verbose output
```

RC and MC run in **parallel** automatically (separate `go test` processes, one per target). Each
process runs its specs sequentially. If you only set one URL, only that target runs.

Each target carries a `DeploymentTarget` field (`"rc"` or `"mc"`) derived from which URL env var was set. Specs
use this to pick the correct `--gather` value and expected tarball layout without hard-coding RC vs
MC — so the same test file is safe under parallel execution (each process only sees one target).

**Step 3 (optional) — Run only RC or only MC.**

```bash
# RC only
export ZOA_RC_API_URL="https://yyyy.lambda-url.us-east-1.on.aws/"
export ZOA_RC_AWS_PROFILE="rrp-regional-dev"
make test-e2e

# MC only
export ZOA_MC_API_URL="https://zzzz.lambda-url.us-east-1.on.aws/"
export ZOA_MC_AWS_PROFILE="rrp-management-dev"
make test-e2e
```

### `must_gather` platform dumps

`ta_mustgather_test.go` exercises async platform collection on the endpoint under test:

| Target | CLI | Tarball marker |
| ------ | --- | -------------- |
| RC | `--gather rc` | `rc/namespaces/platform-api/` |
| MC | `--gather mc` | `mc/namespaces/kube-applier/` |

The happy-path spec runs `must_gather --wait` (up to 20m), downloads `output.tar.gz` via
`zoa download`, and asserts the deployment-specific namespace path exists while the opposite
deployment tree (`mc/` or `rc/`) does not. HCP gather (`gather=hcp`) is covered separately later
via `E2E_HCP_CLUSTER_ID` — not part of the default deep suite yet.

Run only must_gather specs:

```bash
GINKGO_FLAGS='-ginkgo.focus=must_gather' make test-e2e
```

## AWS Credentials

RC and MC are **separate AWS accounts** with separate IAM principals. The test suite handles this
by setting `AWS_PROFILE` on each subprocess call (not globally), so both targets can run in the
same session without conflicting.

### Profile Names

The test suite defaults to `rrp-rc` and `rrp-mc` (the names used inside CI containers and
`make ephemeral-zoa-e2e`). On your host, ephemeral dev accounts use different names:

| Test suite default | Your host profile      | Purpose                               |
| ------------------ | ---------------------- | ------------------------------------- |
| `rrp-rc`           | `rrp-regional-dev`     | RC account — invoke RC ZOA Lambda     |
| `rrp-mc`           | `rrp-management-dev`   | MC account — invoke MC ZOA Lambda     |

Override with `ZOA_RC_AWS_PROFILE` / `ZOA_MC_AWS_PROFILE` when running from your host.
Inside CI containers or `make ephemeral-zoa-e2e`, the defaults (`rrp-rc`/`rrp-mc`) are correct —
no override needed.

The monitoring suite reuses `ZOA_RC_AWS_PROFILE` for SigV4 signing against the RHOBS API
Gateway (which requires RC account credentials).

### How the Test Suite Uses Profiles

The test suite (`test/e2e/helpers_test.go`) sets `AWS_PROFILE` per-subprocess, filtering out any
inherited `AWS_PROFILE`/`ZOA_API_URL` to avoid duplicate keys:

```go
func runZoa(tgt target, args ...string) (string, error) {
    cmd := exec.Command(zoaBin, args...)
    cmd.Env = append(filterEnv(os.Environ(), "AWS_PROFILE", "ZOA_API_URL"),
        "ZOA_API_URL="+tgt.APIURL,
        "AWS_PROFILE="+tgt.AWSProfile,  // rrp-rc or rrp-mc
    )
    out, err := cmd.CombinedOutput()
    return string(out), err
}
```

This ensures:

- RC specs use `rrp-rc` (can invoke RC Lambda)
- MC specs use `rrp-mc` (can invoke MC Lambda)
- No race conditions even with parallel spec execution

### Credential Setup per Scenario

| Scenario                                                   | Who manages credentials                                                                                                                           |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make ephemeral-zoa-e2e ID=...` (from `rosa-hyperfleet`)   | Automatic — `ephemeral-env.sh` maps your host profiles → `rrp-rc`/`rrp-mc` inside the container                                                  |
| `make test-e2e` (from this repo directly)                  | Manual — set `ZOA_RC_AWS_PROFILE=rrp-regional-dev` and `ZOA_MC_AWS_PROFILE=rrp-management-dev`                                                   |
| CI (`nightly-ephemeral`, `on-demand-e2e`)                  | Automatic — Prow mounts a Vault secret with `rrp-rc`/`rrp-mc` profiles pre-configured                                                            |
| Smoke via `rosa-hyperfleet`/`rosa-hyperfleet-api` CI jobs  | Automatic — same Prow secret; `rosa-hyperfleet/ci/e2e-tests.sh` sets profiles, then calls `make test-e2e-smoke`                                   |

## CI: How It Runs

### ZOA's Own Jobs (`openshift/release`)

Config:
[`ci-operator/config/openshift-online/rosa-hyperfleet-zoa/`](https://github.com/openshift/release/blob/master/ci-operator/config/openshift-online/rosa-hyperfleet-zoa/openshift-online-rosa-hyperfleet-zoa-main.yaml)
in `openshift/release`.

**`on-demand-e2e`** (presubmit, `/test on-demand-e2e` on a ZOA PR, `always_run: false`):

ci-operator builds both images (`Containerfile` → `zoa-lambda`, `Containerfile.runner` → `zoa-runner`),
the `rosa-hyperfleet-image-push` step mirrors them to `quay.io/rrp-dev-ci/zoa-{lambda,runner}` with
a `ci-<PR>-<BUILD_ID>` tag, and the provision step overrides `zoa_lambda_image_tag`,
`zoa_runner_image_tag`, `zoa_lambda_source_image`, and `zoa_runner_source_image` in
`config/defaults.yaml` before deploying. The e2e step auto-detects the PR's ZOA repo/branch so
the deep suite runs against the PR's own code and images.

**`nightly-ephemeral`** (periodic): provisions with whatever image tags are pinned in
`rosa-hyperfleet@main`'s config (Konflux-built). Catches infrastructure/platform regressions.

### Consumer Repo Jobs (`rosa-hyperfleet` / `rosa-hyperfleet-api`)

Their `nightly-ephemeral`, `on-demand-e2e`, and `rosa-regionality-compatibility-e2e` jobs call
`rosa-hyperfleet/ci/e2e-tests.sh`, which clones `rosa-hyperfleet-zoa@main` and runs:

- PRs (`JOB_TYPE=presubmit`): `make test-e2e-smoke` (~2min, 6 specs per target)
- Nightlies (`JOB_TYPE=periodic`): `make test-e2e` (full deep suite)

These jobs always test against whatever image tags are pinned in `rosa-hyperfleet` — they never
inject a custom ZOA image.

## Image Management

### How Images Are Published

| System      | When                       | Registry                                                        | Tag format                           | Expiry           |
| ----------- | -------------------------- | --------------------------------------------------------------- | ------------------------------------ | ---------------- |
| Konflux     | Every push to `main`       | `quay.io/redhat-user-workloads/rosa-tenant/zoa-{lambda,runner}` | Full commit SHA (e.g. `8af92e65...`) | Never            |
| ci-operator | ZOA PR `on-demand-e2e` job | `quay.io/rrp-dev-ci/zoa-{lambda,runner}`                        | `ci-<PR>-<BUILD_ID>`                 | No expiry policy |

Konflux builds are the **production** images (with attestation/SAST scans). The `config/defaults.yaml`
pin in `rosa-hyperfleet` always points at a Konflux-built tag.

ci-operator builds are used **only** for pre-merge CI testing.

### How `rosa-hyperfleet` Consumes Image Tags

Four config keys in `rosa-hyperfleet`'s `config/defaults.yaml` control which ZOA images get
deployed: `zoa_lambda_image_tag`, `zoa_runner_image_tag`, `zoa_lambda_source_image`, and
`zoa_runner_source_image`. These flow through `render.py` into Terraform inputs. Lambda images
are mirrored to ECR at deploy time; runner images are pulled directly from Quay by Kubernetes.

See [`rosa-hyperfleet/docs/adding-component-pre-merge.md`](https://github.com/openshift-online/rosa-hyperfleet/blob/main/docs/adding-component-pre-merge.md#multi-image--terraform-deployed-components-zoa)
for the full image override mechanism.

## Troubleshooting

| Symptom                                                                       | Cause                                                            | Fix                                                                                                                         |
| ----------------------------------------------------------------------------- | ---------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `no target configured` test failure                                           | Neither `ZOA_RC_API_URL` nor `ZOA_MC_API_URL` is set             | Export at least one URL before running `make test-e2e`                                                                      |
| MC specs don't run, only RC                                                   | `ZOA_MC_API_URL` not set — env may not have an MC provisioned    | Set `ZOA_MC_API_URL`; if using `make ephemeral-zoa-e2e`, MC should always be available                                      |
| `InvalidSignatureException` or `AccessDeniedException` on MC calls            | Using the RC profile to call the MC Lambda (different AWS account) | Set `ZOA_MC_AWS_PROFILE=rrp-management-dev` (or your MC profile); verify with `AWS_PROFILE=<profile> aws sts get-caller-identity` |
| `InvalidSignatureException` or `AccessDeniedException` on RC calls            | Wrong profile for the RC account                                 | Set `ZOA_RC_AWS_PROFILE=rrp-regional-dev` (or your RC profile); verify with `AWS_PROFILE=<profile> aws sts get-caller-identity`   |
| `make ephemeral-zoa-e2e` fails to clone with a branch-not-found error         | `ZOA_REF` points at a branch that hasn't been pushed             | Push your branch first — this clones from the remote, not your local working tree                                           |
| Monitoring suite fails                                                         | `RHOBS_API_URL` not set                                          | Export `RHOBS_API_URL`; get it from `make ephemeral-list` in `rosa-hyperfleet`. Use `test-e2e-zoa` to skip monitoring.       |
| Monitoring specs fail with `403 Forbidden`                                     | SigV4 signing failed — wrong profile or expired session          | Verify with `awscurl --service execute-api --region us-east-1 "$RHOBS_API_URL/api/v1/query?query=up"` using the RC profile  |
| Monitoring specs timeout on `Eventually` for metrics                           | CloudWatch pipeline lag > 5 min (transient)                      | Retry; if persistent, check YACE pod health: `oc get pod -n cloudwatch-exporter`                                            |
