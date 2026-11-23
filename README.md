# h3llo cloud Terraform Provider

Terraform and OpenTofu provider for [h3llo cloud](https://h3llo.cloud): virtual machines, disks, snapshots and backups,
VPC networks, Elastic IPs and security groups, managed PostgreSQL/MariaDB, DNS zones and records, load balancers,
managed Kubernetes, S3 buckets and SSH keys.

[![Terraform Registry](https://img.shields.io/badge/Terraform-Registry-blueviolet)](https://registry.terraform.io/providers/h3llo-cloud/h3/latest)

## Quick start

```shell
export H3_KEY_ID="<api key id>"
export H3_SECRET_KEY="<api key secret>"
export H3_PROJECT_ID="<project uuid>"   # default project; project_id on resources is then optional
```

```hcl
terraform {
  required_providers {
    h3 = {
      source  = "h3llo-cloud/h3"
      version = "~> 0.3"
    }
  }
}

provider "h3" {} # endpoint https://api.h3llo.cloud, credentials from the environment

resource "h3_vpc" "main" {
  project_id = var.project_id
  name       = "main"
}

resource "h3_network" "app" {
  project_id = var.project_id
  name       = "app"
  vpc_id     = h3_vpc.main.id
  cidr_block = "10.10.0.0/24"
}

resource "h3_ssh_key" "deploy" {
  user_id    = var.user_id # owner of the API key
  name       = "deploy"
  public_key = file(pathexpand("~/.ssh/id_ed25519.pub"))
}

resource "h3_vm" "web" {
  project_id  = var.project_id
  name        = "web-1"
  cpu         = 2
  memory      = "4Gi"
  image       = "ubuntu:24.04"
  disk_size   = "30Gi"
  ssh_key_ids = [h3_ssh_key.deploy.id]
  subnet_name = h3_network.app.subnet_name
}

resource "h3_eip" "web" {
  project_id = var.project_id
  name       = "web-ip"
  vm_id      = h3_vm.web.id
}
```

The complete example is in [`examples/quickstart`](examples/quickstart/main.tf).

## Authentication

Requests are signed with HMAC-SHA256 using an API key (ID + secret) created in the h3llo cloud console.

| Argument | Environment variable | Default |
|---|---|---|
| `api_endpoint` | `H3_API_ENDPOINT` | `https://api.h3llo.cloud` |
| `key_id` | `H3_KEY_ID` | — |
| `secret_key` | `H3_SECRET_KEY` | — |
| `timeout` | `H3_TIMEOUT` | `30` seconds per request |
| `max_retries` | `H3_MAX_RETRIES` | `3` (`0` disables retries) |

## Resources and data sources

| Name | What it manages | In place | Import ID |
|---|---|---|---|
| `h3_vm` | Virtual machine (`user_data`, `placement_group_id`, `deletion_protection`) | `cpu`, `memory`, `eip_id`, `deletion_protection` | `<id>` |
| `h3_vm_placement_group` | Spread placement group (one VM of the group per host) | `name`, `description` | `<project_id>/<id>` or `<id>` |
| `h3_vm_image` | Custom VM image (HTTPS import or copy of a stopped VM's disk) | — | `<project_id>/<id>` or `<id>` |
| `h3_disk` | Block disk | `size` (grow only), `performance_class` | `<id>` |
| `h3_snapshot` | Disk snapshot | — | `<project_id>/<id>` |
| `h3_backup` | Off-site backup of a snapshot | — | `<project_id>/<id>` |
| `h3_vpc` | Project VPC | — | `<id>` |
| `h3_network` | Subnet with NAT gateway | — | `<id>` |
| `h3_eip` | Elastic (public) IP | `vm_id` | `<id>` |
| `h3_database` | PostgreSQL / MariaDB | `cpu`, `memory` (restart), `size` (grow only), `white_ip` | `<id>` |
| `h3_database_pooler` | PgBouncer pooler of a PostgreSQL database (several per database) | `display_name`, `mode`, `instances`, `default`, `paused`, `parameters`, `vpc_mode`/`vpc_port`, `public_port`/`public_via` | `<database_id>/<pooler_id>` |
| `h3_s3_bucket` | S3 bucket | — | `<project_id>/<name>` |
| `h3_ssh_key` | SSH public key | — | `<id>` |
| `h3_security_group` | Security group (stateful firewall) | `name`, `description`, `rule` | `<id>` |
| `h3_security_group_rule` | One rule of a security group | — | `<security_group_id>/<rule_id>` |
| `h3_vm_security_groups` | Security groups of a VM (exclusive list; `[]` = detached) | `security_group_ids` | `<vm_id>` |
| `h3_vm_security_group_attachment` | One security group on a VM (non-exclusive) | — | `<vm_id>/<security_group_id>` |
| `h3_dns_zone` | Authoritative DNS zone | `dnssec`, `default_ttl`, `labels` | `<id>` |
| `h3_dns_recordset` | DNS record set (name + type), conditional writes (ETag) | `records`, `ttl` | `<zone_id>/<name>/<type>` |
| `h3_dns_ptr` | Reverse DNS (PTR) of an Elastic IP | `hostname` | `<project_id>/<eip_id>` |
| `h3_k8s_cluster` | Managed Kubernetes cluster | `kubernetes_version` (upgrade), `control_plane_replicas`, `workers.replicas/cpu/memory_gi`, `autoscaler`, `maintenance_window` | `<project_id>/<id>` |
| `h3_k8s_node_pool` | Additional worker pool of a cluster | `replicas`, `autoscaling`, `labels`, `taint`; rolling: `cpu`, `memory_gi`, `disk_gi`, `max_pods`, `kubelet`; `kubernetes_version` (upgrade) | `<cluster_id>/<name>` or `<project_id>/<cluster_id>/<name>` |
| `h3_lb` | Load balancer | — (h3lb has no update yet) | `<project_id>/<id>` |
| `h3_service_account` | Сервисный аккаунт проекта | `description`, `role`, `disabled` | `<project_id>/<sa_id>` |
| `h3_service_account_key` | API-ключ сервисного аккаунта (секрет только при создании) | — | `<project_id>/<sa_id>/<key_id>` |
| `h3_vm_images` (data) | Image catalog | | |
| `h3_vm_image`, `h3_vm_placement_group` (data) | Custom image, placement group by id or name | | |
| `h3_k8s_versions`, `h3_k8s_cluster`, `h3_k8s_kubeconfig`, `h3_k8s_node_pools` (data) | Kubernetes versions, cluster, kubeconfig, node pools | | |
| `h3_lb`, `h3_lbs` (data) | Load balancer by id/name, list | | |
| `h3_security_group` (data) | Security group by id or name | | |
| `h3_dns_zone` (data) | DNS zone by name | | |
| `h3_dns_nameservers` (data) | h3llo DNS name servers for delegation | | |
| `h3_vpc`, `h3_network`, `h3_eip` (data) | Lookup by id or name (EIP also by address) | | |
| `h3_eips` (data) | EIPs of a project, `bound = false` for free ones (EIP selector) | | |
| `h3_database_poolers` (data) | Poolers of a database, connection budget, limits | | |
| `h3_service_accounts` (data) | Сервисные аккаунты проекта, фильтр по имени | | |
| `h3_iam_audit_events` (data) | Журнал IAM проекта за 90 дней | | |

`h3_ovn_vpc`, `h3_ovn_network` and `h3_ovn_eip` were renamed in 0.3 to `h3_vpc`, `h3_network` and `h3_eip`. The old
names still work as deprecated aliases until 1.0; [docs/guides/migration-0.3.md](docs/guides/migration-0.3.md) shows
how to move state with `moved {}` without recreating anything.

### Platform API availability

All resources use the public API `https://api.h3llo.cloud` with an API key. As of 2026-11-10:

| Service | Resources | On api.h3llo.cloud |
|---|---|---|
| VMs, disks, snapshots, backups | `h3_vm`, `h3_vm_ssh_access`, `h3_disk`, `h3_snapshot`, `h3_backup` | yes |
| VM custom images, placement groups, user-data, deletion protection | `h3_vm_image`, `h3_vm_placement_group`, `h3_vm.user_data` / `placement_group_id` / `deletion_protection` | with the h3vm release that has them (TRD 08/09); before it `404 *_DISABLED` / `400` |
| Networks, EIPs, security groups | `h3_vpc`, `h3_network`, `h3_eip`, `h3_security_group*`, `h3_vm_security_group*` | yes |
| Databases | `h3_database`, `h3_database_user` (replicas, autoscale, users) | yes |
| Database poolers | `h3_database_pooler`, `h3_database_poolers` | pending |
| S3 | `h3_s3_bucket` | yes |
| SSH keys | `h3_ssh_key` | being restored: create keys in the console, use their ids in `h3_vm.ssh_key_ids` |
| DNS | `h3_dns_*`, `h3_dns_nameservers` | yes |
| Kubernetes | `h3_k8s_*` | yes |
| Load balancers | `h3_lb`, `h3_lbs` | yes |
| Сервисные аккаунты, журнал IAM | `h3_service_account`, `h3_service_account_key`, `h3_service_accounts`, `h3_iam_audit_events` | yes (с 14.11.2026): ключу нужен scope `iam:read` (чтение и журнал), `iam:write` (создание и изменение), `iam:delete` (удаление, в том числе `terraform destroy`); `*` IAM не управляет. Ключ сервисного аккаунта читает IAM, но не управляет им |

A pending service answers 404; the provider works with it as soon as the platform enables it, without a new release.

Every other argument change replaces the resource; Terraform shows it as `-/+` in the plan. Replacing `h3_database` or
`h3_disk` **deletes the data**: protect them with `lifecycle { prevent_destroy = true }`.

Full documentation: [Terraform Registry](https://registry.terraform.io/providers/h3llo-cloud/h3/latest/docs) and
[`docs/`](docs/).

## Releases, signatures, mirror

Every release is signed: `terraform-provider-h3_<v>_SHA256SUMS.sig` is a GPG signature by the yearly release key, which
is certified by the h3llo.cloud owner key `98E8 9F85 3412 C315 1E0B 14C9 4F28 F12C 8BE9 F40C` (`keys/`). Terraform and
OpenTofu check it on `init`; by hand: `scripts/verify-release.sh <SHA256SUMS> <SHA256SUMS.sig>`.

If registry.terraform.io or GitHub is not reachable, use our network mirror:

```hcl
# ~/.terraformrc (Terraform) or ~/.tofurc (OpenTofu)
provider_installation {
  network_mirror {
    url     = "https://dl.h3llo.cloud/terraform/"
    include = ["registry.terraform.io/h3llo-cloud/h3", "registry.opentofu.org/h3llo-cloud/h3"]
  }
  direct {
    exclude = ["registry.terraform.io/h3llo-cloud/h3", "registry.opentofu.org/h3llo-cloud/h3"]
  }
}
```

Releases are cut by the CI autopilot (`.gitlab-ci.yml`, `scripts/release.py`): release MR → protected tag → signed build.

## Development

```shell
make build              # terraform-provider-h3 binary
make test               # unit tests and fake-API acceptance tests (terraform in PATH)
make validate-examples  # terraform validate of examples/* against the local build
make docs               # regenerate docs/ (tfplugindocs); CI fails when docs/ is stale
```

Acceptance tests run against an in-memory fake of the public APIs (`internal/acctest`): every resource is created,
updated in place, checked for an empty plan, imported and destroyed, and the request signatures are verified the same
way the API gateway does.

Use a local build with `dev_overrides` in `~/.terraformrc` (see `.terraformrc.example`):

```hcl
provider_installation {
  dev_overrides {
    "h3llo-cloud/h3" = "/path/to/h3terraform/bin"
  }
  direct {}
}
```

`make install-dev` builds into `bin/`. With `dev_overrides` do not run `terraform init` for this provider.

Releases are built in GitLab CI from a protected `vX.Y.Z` tag and signed with the release GPG key. Write changes
under the unreleased section of `CHANGELOG.md`; `scripts/release.py prepare` (CI job `release-mr`) opens the release
MR, merging it creates the tag (`release-tag`), and the tag pipeline publishes the GitHub release that the Terraform
Registry picks up. `scripts/release.py next` shows the version the next release gets.

## License

[MPL-2.0](LICENSE)
