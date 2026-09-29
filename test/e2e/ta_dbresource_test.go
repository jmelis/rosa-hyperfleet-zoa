//go:build e2e

// TA: get_db_resource, list_db_gvks (aws-api scope) — hyperfleet-db readers.
//
// RC-only: the hyperfleet-db lives in the regional cluster VPC, so these TAs
// are registered on the RC Lambda and are skipped for MC targets.
//
// What this suite guarantees vs. what it does not:
//
//   - The BACKBONE specs assert the round-trip only: the TA running in the RC
//     Lambda mints an RDS IAM token, reaches Aurora across the VPC, runs the
//     query against the live schema, and serializes the result. An EMPTY result
//     still proves all of that, so these specs never depend on seeded data. This
//     is exactly the integration unit tests cannot cover.
//   - Projection/filter correctness (summary rows, verbose JSONB decoding, the
//     tombstone filter, age formatting) is covered deterministically by unit
//     tests in pkg/actions, which control the input. We do NOT re-assert it
//     against whatever rows happen to exist here.
//   - A fresh ephemeral RC has no hosted clusters and therefore an empty
//     kubernetes_resources table (MCs/hosted clusters auto-provision only on
//     demand and this suite provisions nothing). So the row/by-name/verbose
//     checks are OPPORTUNISTIC: they add coverage only when this suite is pointed
//     at a populated environment, and Skip otherwise rather than fail.

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// aValidGVK is any well-formed HyperFleet GVK. Used for data-independent specs
// (success/shape, not-found) where the row need not actually exist: a listing
// query for an absent GVK still succeeds with zero rows, and a --name lookup
// still resolves to "not found".
const aValidGVK = "hyperfleet.io/v1alpha1/Cluster"

// firstDBGVK returns a GVK present in the hyperfleet-db (via list_db_gvks) and
// whether any exist, so the opportunistic specs can Skip on an empty DB instead
// of asserting against control-plane state this suite does not create.
func firstDBGVK(tgt target) (string, bool) {
	exec := runAction(tgt, "list_db_gvks")
	for _, item := range outputArray(exec) {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if gvk, ok := row["gvk"].(string); ok && gvk != "" {
			return gvk, true
		}
	}
	return "", false
}

var _ = Describe("list_db_gvks", func() {
	for _, tgt := range targets {
		tgt := tgt
		if tgt.DeploymentTarget != "rc" {
			continue // RC-only: hyperfleet-db is not reachable from MC ZOA
		}

		Describe(tgt.Name, func() {
			// Backbone: proves the RC Lambda can auth to Aurora (RDS IAM), reach
			// it across the VPC, and query the live schema. Succeeding with zero
			// rows is a valid, meaningful pass.
			It("reaches the hyperfleet-db and returns the GVK catalog", func() {
				exec := runAction(tgt, "list_db_gvks")
				for _, item := range outputArray(exec) {
					row, ok := item.(map[string]interface{})
					Expect(ok).To(BeTrue(), "expected each GVK row to be an object, got %T", item)
					Expect(row).To(HaveKey("gvk"))
					Expect(row).To(HaveKey("count"))
					Expect(row["gvk"]).To(BeAssignableToTypeOf(""))
					Expect(row["gvk"]).NotTo(Equal(""))
				}
			})
		})
	}
})

var _ = Describe("get_db_resource", func() {
	for _, tgt := range targets {
		tgt := tgt
		if tgt.DeploymentTarget != "rc" {
			continue // RC-only: hyperfleet-db is not reachable from MC ZOA
		}

		Describe(tgt.Name, func() {
			// --- Backbone: data-independent, always run ---

			It("reaches the hyperfleet-db for a well-formed GVK", func() {
				// A listing query for a GVK with no rows still succeeds and
				// returns an empty array, which is what proves the query path.
				exec := runAction(tgt, "get_db_resource", "--param", "gvk="+aValidGVK, "-A")
				out := exec["output"]
				Expect(out == nil || isJSONArray(out)).To(BeTrue(),
					"expected a JSON array (possibly empty), got %T: %v", out, out)
			})

			It("rejects a request without a gvk parameter", func() {
				out := runActionExpectFailure(tgt, "get_db_resource")
				Expect(out).To(ContainSubstring("gvk"))
			})

			It("rejects a malformed gvk", func() {
				out := runActionExpectFailure(tgt, "get_db_resource", "--param", "gvk=NotAGVK")
				Expect(out).To(ContainSubstring("gvk"))
			})

			It("returns not-found for a name that does not exist", func() {
				// Data-independent: an absent name resolves to not-found whether
				// the GVK has zero rows or many.
				out := runActionExpectFailure(tgt, "get_db_resource",
					"--param", "gvk="+aValidGVK, "-A", "--name", "e2e-nonexistent-db-resource")
				Expect(out).To(ContainSubstring("not found"))
			})

			// --- Opportunistic: only when the environment happens to be populated ---

			It("lists real rows and fetches one by name [needs data]", func() {
				gvk, ok := firstDBGVK(tgt)
				if !ok {
					Skip("hyperfleet-db is empty in this environment (no hosted clusters); " +
						"row-level assertions are covered by unit tests in pkg/actions")
				}

				list := runAction(tgt, "get_db_resource", "--param", "gvk="+gvk, "-A")
				rows := outputArray(list)
				Expect(rows).NotTo(BeEmpty(), "list_db_gvks reported %s exists, so it must be listable", gvk)

				first, ok := rows[0].(map[string]interface{})
				Expect(ok).To(BeTrue())
				Expect(first).To(HaveKey("name"))
				Expect(first).To(HaveKey("state"))
				name, _ := first["name"].(string)
				namespace, _ := first["namespace"].(string)
				Expect(name).NotTo(BeEmpty())

				// A --name lookup resolves to exactly one resource, returned as a
				// single object rather than a one-element array.
				args := []string{"--param", "gvk=" + gvk, "--name", name}
				if namespace != "" {
					args = append(args, "--namespace", namespace)
				}
				single := outputMap(runAction(tgt, "get_db_resource", args...))
				Expect(single["name"]).To(Equal(name))
			})

			It("returns full objects with --verbose [needs data]", func() {
				gvk, ok := firstDBGVK(tgt)
				if !ok {
					Skip("hyperfleet-db is empty in this environment (no hosted clusters); " +
						"verbose projection is covered by TestFullObject in pkg/actions")
				}

				rows := outputArray(runAction(tgt, "get_db_resource", "--param", "gvk="+gvk, "-A", "-v"))
				Expect(rows).NotTo(BeEmpty())

				obj, ok := rows[0].(map[string]interface{})
				Expect(ok).To(BeTrue())
				// Verbose carries the full resource, not the summary row.
				Expect(obj).To(HaveKey("gvk"))
				Expect(obj).To(HaveKey("spec"))
				Expect(obj).To(HaveKey("metadata"))
			})
		})
	}
})

// isJSONArray reports whether a decoded JSON value is an array.
func isJSONArray(v interface{}) bool {
	_, ok := v.([]interface{})
	return ok
}
