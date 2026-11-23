---
page_title: "Upgrading to 0.3"
subcategory: "Guides"
description: |-
  Renamed network resources (h3_ovn_* to h3_vpc, h3_network, h3_eip), the provider default project, SSH keys of h3_vm from h3ssh only, and how to move state without recreating anything.
---

# Upgrading to 0.3

Version 0.3 renames the network resources and adds a default project. Nothing in the cloud has to change: every step
below only edits the configuration and the state. Run `terraform plan` after each step and check that it does not
plan to **destroy** or **create** anything you already have.

## Resource names

Resource and data source names no longer contain the name of the network implementation (`ovn`). The convention is:

* `h3_<service object>`: `h3_vm`, `h3_disk`, `h3_vpc`, `h3_network`, `h3_eip`, `h3_lb`, `h3_k8s_cluster`;
* `h3_<service>_<object>` when the object only exists inside a service: `h3_dns_zone`, `h3_dns_recordset`,
  `h3_security_group_rule`, `h3_s3_bucket`;
* a plural name for list data sources: `h3_eips`, `h3_lbs`;
* references between resources are IDs: `vpc_id`, `network_id`, `eip_id`, `security_group_ids`.

| 0.2 name | 0.3 name | Schema |
|---|---|---|
| `h3_ovn_vpc` | `h3_vpc` | unchanged |
| `h3_ovn_network` | `h3_network` | unchanged |
| `h3_ovn_eip` | `h3_eip` | unchanged |

The old names keep working in 0.3 with a deprecation warning and are removed in 1.0.

## Moving state

### Terraform 1.8 or later, OpenTofu 1.8 or later

Rename the resources in the configuration and add a `moved` block for each:

```terraform
resource "h3_vpc" "main" { # was h3_ovn_vpc
  name = "main"
}

moved {
  from = h3_ovn_vpc.main
  to   = h3_vpc.main
}

moved {
  from = h3_ovn_network.app
  to   = h3_network.app
}

moved {
  from = h3_ovn_eip.web
  to   = h3_eip.web
}
```

`terraform plan` shows `has moved to` with no changes. Apply it, and remove the `moved` blocks once every
workspace that uses the configuration has been applied. The same works inside modules
(`from = module.net.h3_ovn_vpc.main`).

The rename also moves references: replace `h3_ovn_network.app.subnet_name` with `h3_network.app.subnet_name` and so on.

### Terraform 1.7

`moved` between resource types needs Terraform 1.8. On 1.7 use `removed` together with `import`: the old object is
forgotten (not destroyed) and the same cloud object is imported under the new name.

```terraform
removed {
  from = h3_ovn_eip.web
  lifecycle {
    destroy = false
  }
}

import {
  to = h3_eip.web
  id = "<eip id>" # terraform state show h3_ovn_eip.web
}
```

### Terraform 1.5 and 1.6

```shell
terraform state rm h3_ovn_eip.web
terraform import h3_eip.web <eip id>
```

Import reads every argument from the API, so the next plan is empty. The import ID of each resource is on its page.

## Default project

The provider now takes a default project:

```terraform
provider "h3" {
  project_id = "<project uuid>" # or H3_PROJECT_ID
}
```

`project_id` on resources and data sources became optional. When it is omitted, the provider default is used when
the resource is **created**; afterwards the project is kept in state. Changing the provider default later does not
move or replace existing resources. Setting a different `project_id` explicitly on a resource still replaces it,
because objects cannot move between projects.

Configurations that set `project_id` everywhere keep working unchanged.

## SSH keys of h3_vm

`h3_vm` no longer accepts the public key as text: the `ssh_key` argument is removed. Every key is an h3ssh key
(`h3_ssh_key`, or a key added in the console), and the VM refers to keys by id:

```terraform
# before (0.2)
resource "h3_vm" "web" {
  # ...
  ssh_key = "ssh-ed25519 AAAA... me@laptop"
}

# after (0.3)
resource "h3_ssh_key" "me" {
  user_id    = var.user_id
  name       = "me-laptop"
  public_key = "ssh-ed25519 AAAA... me@laptop"
}

resource "h3_vm" "web" {
  # ...
  ssh_key_ids = [h3_ssh_key.me.id]
}
```

What happens to existing VMs:

- The provider upgrades the state on the first `plan` (schema version 1): `ssh_key` is dropped from the state, nothing
  is sent to the API. The key stays on the VM.
- `ssh_key_ids` is your own grant on the VM. After the upgrade the provider reads it from the grant made at VM
  creation; if someone else created the VM, Terraform sets your grant on the first apply.
- `ssh_key_ids` and the deprecated `ssh_key_id` change **in place**. In 0.2 any key change replaced the VM with its
  root disk. `ssh_key_id = "<id>"` keeps working with a warning; replace it with `ssh_key_ids = ["<id>"]` (the plan
  shows an update without API calls).
- Keys of other project members are granted with the new `h3_vm_ssh_access` resource.
- Live key changes need `ssh_access_managed = true` (the default for new VMs on images with qemu-guest-agent). A VM
  created before this release has it off. **Turning it on restarts a running VM once**, and from then on the platform
  owns `/home/user/.ssh/authorized_keys`: keys written there by hand are removed. Keep keys that are not managed by
  the platform in `~/.ssh/authorized_keys2`.

## Other changes

See the [CHANGELOG](https://github.com/h3llo-cloud/terraform-provider-h3/blob/main/CHANGELOG.md) for new resources and
data sources (DNS, security groups, load balancers, managed Kubernetes) and their API requirements.
