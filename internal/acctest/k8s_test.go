package acctest_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func k8sConfig(f *acctest.Fake, version string, replicas, cpu, mem int, extra string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_k8s_cluster" "main" {
  project_id         = %q
  name               = "prod"
  kubernetes_version = %q

  workers = {
    replicas  = %d
    cpu       = %d
    memory_gi = %d
    disk_gi   = 50
  }
%s
}
`, acctest.Project, version, replicas, cpu, mem, extra))
}

func countCalls(f *acctest.Fake, prefix string) int { return len(f.Calls(prefix)) }

// Version change = in-place upgrade (POST /upgrade), never a replacement;
// workers and control plane scale in place; downgrade refused at plan time.
func TestK8s_ClusterLifecycle(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var id string
	clusterPath := func() string { return "/api/k8s/v3/clusters/" + id }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("k8s"); n != 0 {
				return fmt.Errorf("%d clusters left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "1.34.1", 2, 2, 4, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error { id = f.Only("k8s"); return nil },
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "status", "READY"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "kubernetes_version", "1.34.1"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "cluster_type", "DEV"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "control_plane_replicas", "1"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "pod_cidr", "10.244.0.0/16"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "endpoint", "https://prod.k8s.h3llo.cloud:6443"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "cluster_ca_certificate", acctest.FakeKubeconfigCA),
					resource.TestMatchResourceAttr("h3_k8s_cluster.main", "kubeconfig", regexp.MustCompile("kind: Config")),
					resource.TestCheckNoResourceAttr("h3_k8s_cluster.main", "autoscaler"),
				),
			},
			{
				// "v1.34.1" is the same version as "1.34.1": only the spelling is recorded, no upgrade.
				Config: k8sConfig(f, "v1.34.1", 2, 2, 4, ""),
				Check: func(*terraform.State) error {
					if n := countCalls(f, "POST "+clusterPath()+"/upgrade"); n != 0 {
						return fmt.Errorf("spelling change called upgrade %d times", n)
					}
					return nil
				},
			},
			{
				// Upgrade in place.
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_k8s_cluster.main", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "kubernetes_version", "v1.35.9"),
					func(*terraform.State) error {
						if n := countCalls(f, "POST "+clusterPath()+"/upgrade"); n != 1 {
							return fmt.Errorf("upgrade calls = %d, want 1", n)
						}
						if n := countCalls(f, "POST /api/k8s/v3/clusters"); n != 2 { // create + upgrade
							return fmt.Errorf("POST calls = %d, want 2 (no re-create)", n)
						}
						return nil
					},
				),
			},
			{
				// Scale workers and the control plane in place.
				Config: k8sConfig(f, "v1.35.9", 3, 4, 8, "  control_plane_replicas = 3\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_k8s_cluster.main", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "workers.replicas", "3"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "workers.cpu", "4"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "control_plane_replicas", "3"),
					func(*terraform.State) error {
						if n := countCalls(f, "PATCH "+clusterPath()+"/workers"); n != 1 {
							return fmt.Errorf("workers PATCH = %d, want 1", n)
						}
						return nil
					},
				),
			},
			{
				// Downgrade is refused before any API call.
				Config:      k8sConfig(f, "v1.34.1", 3, 4, 8, "  control_plane_replicas = 3\n"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("downgrade is not supported"),
			},
			{
				// No drift after scaling.
				Config:   k8sConfig(f, "v1.35.9", 3, 4, 8, "  control_plane_replicas = 3\n"),
				PlanOnly: true,
			},
			{
				ResourceName:      "h3_k8s_cluster.main",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return acctest.Project + "/" + id, nil },
			},
		},
	})
}

// With the autoscaler on, a replica count changed by the autoscaler is not drift.
func TestK8s_AutoscalerOwnsReplicas(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	as := "  autoscaler = {\n    enabled  = true\n    max_size = 6\n  }\n"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "v1.36.5", 2, 2, 4, as),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "autoscaler.max_size", "6"),
					func(*terraform.State) error { f.K8sAutoscale(f.Only("k8s"), 5); return nil },
				),
			},
			{Config: k8sConfig(f, "v1.36.5", 2, 2, 4, as), PlanOnly: true},
			{
				// Disabling the autoscaler is an in-place update.
				Config: k8sConfig(f, "v1.36.5", 2, 2, 4, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_k8s_cluster.main", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("h3_k8s_cluster.main", "autoscaler"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "workers.replicas", "2"),
				),
			},
		},
	})
}

func TestK8s_DiskChangeReplaces_DataSources(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	ds := `
data "h3_k8s_versions" "all" {}

data "h3_k8s_versions" "from" {
  from = "1.34.1"
}

data "h3_k8s_kubeconfig" "main" {
  cluster_id = h3_k8s_cluster.main.id
  project_id = h3_k8s_cluster.main.project_id
}

data "h3_k8s_cluster" "main" {
  id         = h3_k8s_cluster.main.id
  project_id = h3_k8s_cluster.main.project_id
}
`
	cfg := k8sConfig(f, "v1.34.1", 1, 2, 4, "") + ds
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.h3_k8s_versions.all", "versions.#", "3"),
					resource.TestCheckResourceAttr("data.h3_k8s_versions.all", "latest", "v1.36.5"),
					resource.TestCheckResourceAttr("data.h3_k8s_versions.from", "upgrade_targets.#", "1"),
					resource.TestCheckResourceAttr("data.h3_k8s_versions.from", "next_upgrade", "v1.35.9"),
					resource.TestCheckResourceAttr("data.h3_k8s_kubeconfig.main", "endpoint", "https://prod.k8s.h3llo.cloud:6443"),
					resource.TestCheckResourceAttrPair("data.h3_k8s_kubeconfig.main", "kubeconfig", "h3_k8s_cluster.main", "kubeconfig"),
					resource.TestCheckResourceAttr("data.h3_k8s_cluster.main", "worker_replicas", "1"),
					resource.TestCheckResourceAttr("data.h3_k8s_cluster.main", "status", "READY"),
				),
			},
			{
				Config: strings.Replace(cfg, "disk_gi   = 50", "disk_gi   = 80", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_k8s_cluster.main", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
}

func TestK8s_ValidateConfig(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: k8sConfig(f, "v1.34.1", 1, 2, 4, "  control_plane_replicas = 2\n"), PlanOnly: true, ExpectError: regexp.MustCompile("must be 1, 3 or 5")},
			{Config: k8sConfig(f, "latest", 1, 2, 4, ""), PlanOnly: true, ExpectError: regexp.MustCompile("expected a version like")},
			{Config: k8sConfig(f, "v1.34.1", 1, 2, 4, "  autoscaler = { enabled = true }\n"), PlanOnly: true, ExpectError: regexp.MustCompile("max_size is required")},
		},
	})
	if n := len(f.Calls("")); n != 0 {
		t.Fatalf("validation reached the API: %v", f.Calls(""))
	}
}

// node_local_dns_cache: not sent on create when off, toggled in place with
// PATCH /node-local-dns-cache (async ENABLING/DISABLING), never a replacement.
func TestK8s_NodeLocalDNSCacheInPlace(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var id string
	var createBodies []acctest.Obj
	f.Hook("POST /api/k8s/v3/clusters", func(_ http.ResponseWriter, q *acctest.Req) bool {
		if q.R.URL.Path == "/api/k8s/v3/clusters" {
			createBodies = append(createBodies, q.JSON())
		}
		return false
	})
	nld := func(v bool) string { return fmt.Sprintf("  node_local_dns_cache = %t\n", v) }
	patches := func() int { return countCalls(f, "PATCH /api/k8s/v3/clusters/"+id+"/node-local-dns-cache") }
	nldState := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			n, _ := f.Get("k8s", id)["nodeLocalDNSCache"].(acctest.Obj)
			if got := fmt.Sprint(n["state"]); got != want {
				return fmt.Errorf("fake nodeLocalDNSCache.state = %s, want %s", got, want)
			}
			return nil
		}
	}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("h3_k8s_cluster.main", plancheck.ResourceActionUpdate),
	}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error { id = f.Only("k8s"); return nil },
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "node_local_dns_cache", "false"),
					func(*terraform.State) error {
						if len(createBodies) != 1 {
							return fmt.Errorf("create calls = %d", len(createBodies))
						}
						if _, ok := createBodies[0]["nodeLocalDNSCache"]; ok {
							return fmt.Errorf("nodeLocalDNSCache sent while off: %v", createBodies[0])
						}
						return nil
					},
				),
			},
			{
				Config:           k8sConfig(f, "v1.35.9", 2, 2, 4, nld(true)),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "node_local_dns_cache", "true"),
					resource.TestCheckResourceAttrPtr("h3_k8s_cluster.main", "id", &id),
					nldState("READY"),
					func(*terraform.State) error {
						if n := patches(); n != 1 {
							return fmt.Errorf("node-local-dns-cache PATCH = %d, want 1", n)
						}
						return nil
					},
				),
			},
			{Config: k8sConfig(f, "v1.35.9", 2, 2, 4, nld(true)), PlanOnly: true},
			{
				ResourceName:      "h3_k8s_cluster.main",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return acctest.Project + "/" + id, nil },
			},
			{
				// Removing the attribute turns the cache off (default false), in place.
				Config:           k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "node_local_dns_cache", "false"),
					resource.TestCheckResourceAttrPtr("h3_k8s_cluster.main", "id", &id),
					nldState("DISABLED"),
					func(*terraform.State) error {
						if n := patches(); n != 2 {
							return fmt.Errorf("node-local-dns-cache PATCH = %d, want 2", n)
						}
						if n := len(createBodies); n != 1 {
							return fmt.Errorf("cluster re-created: %d creates", n)
						}
						return nil
					},
				),
			},
			{Config: k8sConfig(f, "v1.35.9", 2, 2, 4, nld(false)), PlanOnly: true},
		},
	})
}

// node_local_dns_cache = true on create: sent in the body, waited for READY.
func TestK8s_NodeLocalDNSCacheOnCreate(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var sent any
	f.Hook("POST /api/k8s/v3/clusters", func(_ http.ResponseWriter, q *acctest.Req) bool {
		if q.R.URL.Path == "/api/k8s/v3/clusters" {
			sent = q.JSON()["nodeLocalDNSCache"]
		}
		return false
	})
	ds := `
data "h3_k8s_cluster" "main" {
  id         = h3_k8s_cluster.main.id
  project_id = h3_k8s_cluster.main.project_id
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config: k8sConfig(f, "v1.35.9", 2, 2, 4, "  node_local_dns_cache = true\n") + ds,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("h3_k8s_cluster.main", "node_local_dns_cache", "true"),
				resource.TestCheckResourceAttr("data.h3_k8s_cluster.main", "node_local_dns_cache", "true"),
				resource.TestCheckResourceAttr("data.h3_k8s_cluster.main", "node_local_dns_cache_state", "READY"),
				func(*terraform.State) error {
					if fmt.Sprint(sent) != "map[enabled:true]" {
						return fmt.Errorf("create body nodeLocalDNSCache = %v", sent)
					}
					if n := countCalls(f, "PATCH "); n != 0 {
						return fmt.Errorf("PATCH calls on create: %d", n)
					}
					return nil
				},
			),
		}},
	})
}

// Platform refusals are shown with reason and message; a conflict is not
// retried; a failed rollout (state ERROR) fails the apply with its message.
func TestK8s_NodeLocalDNSCacheErrors(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var id string
	on := k8sConfig(f, "v1.35.9", 2, 2, 4, "  node_local_dns_cache = true\n")
	patches := func() int { return countCalls(f, "PATCH /api/k8s/v3/clusters/"+id+"/node-local-dns-cache") }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				Check: func(*terraform.State) error {
					id = f.Only("k8s")
					f.K8sNodeLocalDNSReject(id, 409, "NODE_LOCAL_DNS_CONFLICT", "kube-system/node-local-dns already exists")
					return nil
				},
			},
			{
				Config:      on,
				ExpectError: regexp.MustCompile(`(?s)NODE_LOCAL_DNS_CONFLICT.*node-local-dns already exists`),
			},
			{
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				Check: func(*terraform.State) error {
					if n := patches(); n != 1 {
						return fmt.Errorf("conflict PATCH = %d, want 1 (no retry)", n)
					}
					f.K8sNodeLocalDNSReject(id, 422, "NODE_LOCAL_DNS_UNSUPPORTED", "Cilium 1.15 does not support it")
					return nil
				},
			},
			{
				Config:      on,
				ExpectError: regexp.MustCompile(`(?s)NODE_LOCAL_DNS_UNSUPPORTED.*Cilium 1\.15 does not support it`),
			},
			{
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				Check: func(*terraform.State) error {
					f.K8sNodeLocalDNSReject(id, 0, "", "")
					f.K8sNodeLocalDNSFail(id, "node-local-dns DaemonSet is not ready on 1/2 nodes")
					return nil
				},
			},
			{
				Config:      on,
				ExpectError: regexp.MustCompile(`(?s)ERROR state.*DaemonSet is not ready on 1/2 nodes`),
			},
			{
				// The failed rollout is visible as enabled; turning it off recovers.
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "node_local_dns_cache", "false"),
					func(*terraform.State) error {
						if got := f.Get("k8s", id)["nodeLocalDNSCache"].(acctest.Obj)["state"]; got != "DISABLED" {
							return fmt.Errorf("state = %v", got)
						}
						return nil
					},
				),
			},
		},
	})
}

// An h3k8s without the feature: off works (no field sent, no drift), on fails clearly.
func TestK8s_NodeLocalDNSCacheOldBackend(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.K8sNoNodeLocalDNS = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""),
				Check:  resource.TestCheckResourceAttr("h3_k8s_cluster.main", "node_local_dns_cache", "false"),
			},
			{Config: k8sConfig(f, "v1.35.9", 2, 2, 4, ""), PlanOnly: true},
			{
				Config:      k8sConfig(f, "v1.35.9", 2, 2, 4, "  node_local_dns_cache = true\n"),
				ExpectError: regexp.MustCompile(`does not support NodeLocal DNSCache`),
			},
		},
	})
}

// network_id places the workers in a network of the project VPC; a change replaces the cluster.
// Without network_id the platform default is reported in `network` and does not cause drift.
func TestK8s_NetworkID(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "1.35.9", 1, 2, 4, `  network_id = "net-a"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "network_id", "net-a"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "network.mode", "vpc"),
					// the fake reports mode vpc only when the create request carried networkId
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "network.network_id", "net-a"),
				),
			},
			{
				Config: k8sConfig(f, "1.35.9", 1, 2, 4, `  network_id = "net-b"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_k8s_cluster.main", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("h3_k8s_cluster.main", "network.network_id", "net-b"),
			},
		},
	})
}

func TestK8s_NetworkDefaultShared(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: k8sConfig(f, "1.35.9", 1, 2, 4, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("h3_k8s_cluster.main", "network_id"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "network.mode", "shared"),
				),
			},
			{
				Config:             k8sConfig(f, "1.35.9", 1, 2, 4, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
