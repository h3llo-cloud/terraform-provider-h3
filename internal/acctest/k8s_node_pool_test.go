package acctest_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// poolConfig: a cluster (workers = pool "default") plus one h3_k8s_node_pool.
func poolConfig(f *acctest.Fake, version, pool string) string {
	return k8sConfig(f, version, 2, 2, 4, "") + fmt.Sprintf(`
resource "h3_k8s_node_pool" "np" {
  project_id = %q
  cluster_id = h3_k8s_cluster.main.id
%s
}
`, acctest.Project, pool)
}

const poolFixed = `
  name      = "api"
  cpu       = 4
  memory_gi = 8
  replicas  = 2
  labels    = { workload = "api" }

  taint {
    key    = "dedicated"
    value  = "api"
    effect = "NoSchedule"
  }
`

// Lifecycle: create, labels/taints in place without a rollout, cpu/memory in
// place (rolling, no replacement), switch to autoscaling (replicas follows
// min_size, autoscaler changes are not drift), back to a fixed size, import,
// rename = replace, destroy removes the pool.
func TestK8s_NodePoolLifecycle(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var cluster string
	poolPath := func() string { return "/api/k8s/v3/clusters/" + cluster + "/node-pools" }
	addr := "h3_k8s_node_pool.np"
	autoscaled := strings.Replace(poolFixed, "  replicas  = 2\n", "  autoscaling = {\n    enabled  = true\n    min_size = 1\n    max_size = 5\n    scale_down_utilization_threshold = \"0.50\"\n  }\n", 1)
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
				Config: poolConfig(f, "v1.35.9", poolFixed),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error { cluster = f.Only("k8s"); return nil },
					resource.TestCheckResourceAttrPair(addr, "cluster_id", "h3_k8s_cluster.main", "id"),
					resource.TestCheckResourceAttr(addr, "status", "READY"),
					resource.TestCheckResourceAttr(addr, "replicas", "2"),
					resource.TestCheckResourceAttr(addr, "current_replicas", "2"),
					resource.TestCheckResourceAttr(addr, "disk_gi", "50"),
					resource.TestCheckResourceAttr(addr, "disk_profile", "replicated"),
					resource.TestCheckResourceAttr(addr, "kubernetes_version", "v1.35.9"),
					resource.TestCheckResourceAttr(addr, "labels.workload", "api"),
					resource.TestCheckResourceAttr(addr, "taint.0.effect", "NoSchedule"),
					resource.TestCheckNoResourceAttr(addr, "max_pods"),
					resource.TestCheckNoResourceAttr(addr, "autoscaling"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources[addr].Primary.ID; id != cluster+"/api" {
							return fmt.Errorf("id = %q", id)
						}
						return nil
					},
				),
			},
			{
				// Labels and taints: one PATCH, in place.
				Config: poolConfig(f, "v1.35.9", strings.Replace(strings.Replace(poolFixed, `workload = "api"`, `workload = "api", tier = "web"`, 1),
					`effect = "NoSchedule"`, `effect = "PreferNoSchedule"`, 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "labels.tier", "web"),
					resource.TestCheckResourceAttr(addr, "taint.0.effect", "PreferNoSchedule"),
					func(*terraform.State) error {
						if n := countCalls(f, "PATCH "+poolPath()+"/api"); n != 1 {
							return fmt.Errorf("PATCH = %d, want 1", n)
						}
						return nil
					},
				),
			},
			{
				// Size: rolling replacement of the nodes, in place.
				Config: poolConfig(f, "v1.35.9", strings.Replace(strings.Replace(poolFixed, "cpu       = 4", "cpu       = 8", 1), "memory_gi = 8", "memory_gi = 16", 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cpu", "8"),
					resource.TestCheckResourceAttr(addr, "memory_gi", "16"),
					resource.TestCheckResourceAttr(addr, "labels.%", "1"),
					func(*terraform.State) error {
						if n := countCalls(f, "POST "+poolPath()); n != 1 {
							return fmt.Errorf("pool POST = %d, want 1 (no re-create)", n)
						}
						return nil
					},
				),
			},
			{
				// Autoscaling: replicas is not configured and follows min_size.
				Config: poolConfig(f, "v1.35.9", autoscaled),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "replicas", "1"),
					resource.TestCheckResourceAttr(addr, "autoscaling.max_size", "5"),
					resource.TestCheckResourceAttr(addr, "autoscaling.scale_down_utilization_threshold", "0.50"),
					func(*terraform.State) error { f.K8sPoolAutoscale(cluster, "api", 4); return nil },
				),
			},
			{
				// The autoscaler grew the pool to 4: not drift.
				Config:   poolConfig(f, "v1.35.9", autoscaled),
				PlanOnly: true,
			},
			{
				Config: poolConfig(f, "v1.35.9", autoscaled),
				Check:  resource.TestCheckResourceAttr(addr, "current_replicas", "4"),
			},
			{
				// Autoscaling off: the configured size applies.
				Config: poolConfig(f, "v1.35.9", strings.Replace(poolFixed, "replicas  = 2", "replicas  = 3", 1)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "autoscaling"),
					resource.TestCheckResourceAttr(addr, "replicas", "3"),
					resource.TestCheckResourceAttr(addr, "current_replicas", "3"),
					resource.TestCheckResourceAttr(addr, "cpu", "4"),
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"status", "current_replicas", "billed_replicas"},
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return acctest.Project + "/" + cluster + "/api", nil },
			},
			{
				// Rename = new pool.
				Config: poolConfig(f, "v1.35.9", strings.Replace(poolFixed, `name      = "api"`, `name      = "web"`, 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: func(*terraform.State) error {
					if n := countCalls(f, "DELETE "+poolPath()+"/api"); n != 1 {
						return fmt.Errorf("DELETE api = %d, want 1", n)
					}
					return nil
				},
			},
		},
	})
}

// A cluster upgrade moves every pool: a pool that follows the cluster
// version is not upgraded again, an unset version is not drift; a pool left
// behind is upgraded with POST .../upgrade; a downgrade is refused at plan time.
func TestK8s_NodePoolUpgrade(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var cluster string
	upgradePath := func() string { return "POST /api/k8s/v3/clusters/" + cluster + "/node-pools/api/upgrade" }
	follow := poolFixed + "  kubernetes_version = h3_k8s_cluster.main.kubernetes_version\n"
	pinned := func(v string) string { return poolFixed + fmt.Sprintf("  kubernetes_version = %q\n", v) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: poolConfig(f, "v1.34.1", follow),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error { cluster = f.Only("k8s"); return nil },
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "kubernetes_version", "v1.34.1"),
				),
			},
			{
				Config: poolConfig(f, "v1.35.9", follow),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "kubernetes_version", "v1.35.9"),
					func(*terraform.State) error {
						if n := countCalls(f, upgradePath()); n != 0 {
							return fmt.Errorf("pool upgrade calls = %d, want 0 (the cluster upgrade moved it)", n)
						}
						return nil
					},
				),
			},
			{
				// Version not configured: the cluster upgrade is not drift of the pool.
				Config: poolConfig(f, "v1.36.5", poolFixed),
				Check:  resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "kubernetes_version", "v1.35.9"),
			},
			{Config: poolConfig(f, "v1.36.5", poolFixed), PlanOnly: true},
			{
				Config: poolConfig(f, "v1.36.5", poolFixed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "kubernetes_version", "v1.36.5"),
					func(*terraform.State) error { f.K8sSetPoolVersion(cluster, "api", "v1.35.9"); return nil },
				),
			},
			{
				// The pool runs one step below the control plane: upgrade it in place.
				Config: poolConfig(f, "v1.36.5", pinned("1.36.5")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_k8s_node_pool.np", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "kubernetes_version", "1.36.5"),
					func(*terraform.State) error {
						if n := countCalls(f, upgradePath()); n != 1 {
							return fmt.Errorf("pool upgrade calls = %d, want 1", n)
						}
						return nil
					},
				),
			},
			{
				Config:      poolConfig(f, "v1.36.5", pinned("v1.35.9")),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("downgrade is not supported"),
			},
		},
	})
}

func TestK8s_NodePoolValidateConfig(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	base := "  name      = \"api\"\n  cpu       = 2\n  memory_gi = 4\n"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: poolConfig(f, "v1.35.9", base), PlanOnly: true, ExpectError: regexp.MustCompile("replicas is required unless autoscaling")},
			{Config: poolConfig(f, "v1.35.9", base+"  replicas = 2\n  autoscaling = {\n    enabled  = true\n    min_size = 1\n    max_size = 3\n  }\n"),
				PlanOnly: true, ExpectError: regexp.MustCompile("leave replicas unset")},
			{Config: poolConfig(f, "v1.35.9", base+"  autoscaling = {\n    enabled = true\n  }\n"),
				PlanOnly: true, ExpectError: regexp.MustCompile("min_size is required")},
			{Config: poolConfig(f, "v1.35.9", strings.Replace(base, `"api"`, `"Api_1"`, 1)+"  replicas = 1\n"),
				PlanOnly: true, ExpectError: regexp.MustCompile("lowercase letters")},
			{Config: poolConfig(f, "v1.35.9", strings.Replace(base, "memory_gi = 4", "memory_gi = 64", 1)+"  replicas = 1\n"),
				PlanOnly: true, ExpectError: regexp.MustCompile("1-16 GiB per vCPU")},
			{Config: poolConfig(f, "v1.35.9", base+"  replicas = 1\n  taint {\n    key    = \"a\"\n    effect = \"NoWay\"\n  }\n"),
				PlanOnly: true, ExpectError: regexp.MustCompile("Invalid taint effect")},
		},
	})
	if n := len(f.Calls("")); n != 0 {
		t.Fatalf("validation reached the API: %v", f.Calls(""))
	}
}

// Import by "<cluster_id>/<name>" with the provider default project, the data
// source lists "default" (the workers) and the added pools, and the cluster-wide
// autoscaler flags are sent with PATCH /autoscaler (not returned: no drift).
func TestK8s_NodePoolsDataSource_ImportDefaultProject_Expander(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var cluster string
	cfg := `
resource "h3_k8s_cluster" "main" {
  name               = "prod"
  kubernetes_version = "v1.35.9"
  workers = {
    replicas  = 2
    cpu       = 2
    memory_gi = 4
    disk_gi   = 50
  }
  autoscaler = {
    enabled                     = false
    expander                    = "least-waste"
    balance_similar_node_groups = true
  }
}

resource "h3_k8s_node_pool" "np" {
  cluster_id = h3_k8s_cluster.main.id
  name       = "batch"
  cpu        = 4
  memory_gi  = 16
  disk_gi    = 100
  max_pods   = 60
  autoscaling = {
    enabled  = true
    min_size = 0
    max_size = 4
  }
  kubelet = {
    platform_defaults = true
    eviction_hard     = { "memory.available" = "300Mi" }
  }
  taint {
    key    = "batch"
    effect = "NoSchedule"
  }
}

data "h3_k8s_node_pools" "all" {
  cluster_id = h3_k8s_node_pool.np.cluster_id
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: f.ConfigWithProject(acctest.Project, cfg),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error { cluster = f.Only("k8s"); return nil },
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "replicas", "0"),
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "max_pods", "60"),
					resource.TestCheckResourceAttr("h3_k8s_node_pool.np", "kubelet.eviction_hard.memory.available", "300Mi"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.#", "2"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.0.name", "default"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.0.replicas", "2"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.1.name", "batch"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.1.autoscaling_enabled", "true"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.1.autoscaling_max_size", "4"),
					resource.TestCheckResourceAttr("data.h3_k8s_node_pools.all", "node_pools.1.taints.0.key", "batch"),
					resource.TestCheckResourceAttr("h3_k8s_cluster.main", "autoscaler.expander", "least-waste"),
					func(*terraform.State) error {
						if n := countCalls(f, "PATCH /api/k8s/v3/clusters/"+cluster+"/autoscaler"); n != 1 {
							return fmt.Errorf("autoscaler PATCH = %d, want 1", n)
						}
						return nil
					},
				),
			},
			{Config: f.ConfigWithProject(acctest.Project, cfg), PlanOnly: true},
			{
				Config: f.ConfigWithProject(acctest.Project, strings.Replace(cfg, `"least-waste"`, `"most-pods"`, 1)),
				Check: func(*terraform.State) error {
					if n := countCalls(f, "PATCH /api/k8s/v3/clusters/"+cluster+"/autoscaler"); n != 2 {
						return fmt.Errorf("autoscaler PATCH = %d, want 2", n)
					}
					return nil
				},
			},
			{
				Config:                  f.ConfigWithProject(acctest.Project, strings.Replace(cfg, `"least-waste"`, `"most-pods"`, 1)),
				ResourceName:            "h3_k8s_node_pool.np",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"status", "current_replicas", "billed_replicas"},
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return cluster + "/batch", nil },
			},
		},
	})
}
