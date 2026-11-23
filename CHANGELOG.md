# Changelog

All notable changes to the h3llo cloud Terraform provider will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-11-24

Upgrade guide: `docs/guides/migration-0.3.md`. Nothing in 0.3.0 requires recreating resources.

### Platform API availability

Every resource talks to the public API `https://api.h3llo.cloud` with an API key. Since 2026-11-02 the DNS API
(`h3_dns_*`, `h3_dns_nameservers`) and the Kubernetes API (`h3_k8s_*`; node pools, `api_allowed_cidrs` and `node_local_dns_cache` need the matching h3k8s
release) answer there with API keys, since 2026-11-10 the load balancer API too (`h3_lb`, data `h3_lb`, `h3_lbs`).
The services below are not yet reachable, so their resources fail with a 404 (or
503) until the platform enables them; the provider needs no new release for that:

- `h3_ssh_key`: the public h3ssh API is being restored; until then create keys in the console and pass their ids to
  `h3_vm.ssh_key_ids`;
- `h3_vm_security_group_attachment` works now through a read-modify-write fallback and switches to the race-free
  point attach by itself once the platform offers it.

### Breaking changes

- **h3_vm:** `ssh_key` (the public key as text) is removed: SSH keys come from h3ssh only. Create the key with
  `h3_ssh_key` (or in the console) and set `ssh_key_ids = [h3_ssh_key.k.id]`. The state is upgraded automatically
  (schema version 1, `ssh_key` is dropped, no API calls); the key already on the VM stays. See the
  "SSH keys of h3_vm" section of the upgrade guide.

### Deprecated

- **h3_vm `ssh_key_id`:** use `ssh_key_ids = [<id>]`. It keeps working (in place, no longer replaces the VM) and is
  removed in 1.0.
- **h3_ovn_vpc, h3_ovn_network, h3_ovn_eip** are renamed to **h3_vpc**, **h3_network** and **h3_eip** (same schema
  and behaviour). The old names keep working with a deprecation warning and are removed in 1.0. Move state with
  `moved { from = h3_ovn_vpc.main  to = h3_vpc.main }` (Terraform/OpenTofu >= 1.8, no API calls, plan shows no
  changes); on Terraform 1.7 use `removed { lifecycle { destroy = false } }` + `import {}`; on 1.5/1.6
  `terraform state rm` + `terraform import`.

### Added

- **h3_database_user: `system_roles` и `settings` (PostgreSQL, h3db PG-ROLES-PARAMS).** `system_roles` — встроенные
  роли (`pg_monitor`, `pg_read_all_stats`, `pg_read_all_settings`, `pg_stat_scan_tables`, `pg_signal_backend`,
  `pg_read_all_data`), `settings` — параметры новых сессий пользователя (`default_transaction_isolation`,
  `statement_timeout`, `lock_timeout`, `idle_in_transaction_session_timeout`, `idle_session_timeout`, `work_mem`).
  Меняются на месте без рестарта. Не заданы в конфигурации — не управляются (роли из консоли остаются). Значения
  пишутся так, как их возвращает API (`30s`, `64MB`), иначе ошибка на этапе plan. Пресет `monitor` в `grants`.

- **Сервисные аккаунты и журнал IAM (h3iam `/api/iam/v1`).** `h3_service_account` (роль в проекте, описание и
  выключение меняются на месте), `h3_service_account_key` (статический HMAC-ключ со scope и сроком; `secret_key` есть
  только у ключа, созданного этим Terraform, и помечен как чувствительный; любое изменение выпускает новый ключ),
  data `h3_service_accounts` и `h3_iam_audit_events` (события за 90 дней). Пока проект не подключён к API, провайдер
  выдаёт ошибку и не удаляет аккаунты и ключи из состояния. Пример: `examples/iam`. Ключу нужны scopes `iam:read`,
  `iam:write`, `iam:delete` (`*` IAM не управляет); работает на api.h3llo.cloud с 14.11.2026.
- **Compute parity with Yandex Compute (h3vm TRD 08/09).**
  - **h3_vm `user_data`** (and `user_data_encoding`: `plain` | `base64`): your cloud-init user-data (`#cloud-config`,
    `#!` script or `#cloud-boothook`, up to 64 KiB), run once on the first boot after the platform setup. The API never
    returns it: the provider keeps the configured value in state, never logs it, and a change replaces the VM. Checked at
    plan time (first line, size, base64, no gzip; not with `source_snapshot_id`/`source_backup_id`).
  - **h3_vm `deletion_protection`** (default `false`): changed in place without a restart; while it is on, destroy and
    replacement fail with a clear message (409 `VM_DELETION_PROTECTED`).
  - **h3_vm `placement_group_id`** and the new resource **h3_vm_placement_group** (strategy `spread`: one VM of the group
    per host; `name`, `description` in place; `vm_ids`, `max_vms` computed; a non-empty group cannot be deleted). Data
    source **h3_vm_placement_group** by id or name.
  - New resource **h3_vm_image**: a custom image imported from an HTTPS URL (`url`, `size_gi`, qcow2/raw, `.gz`/`.xz`) or
    copied from the root disk of a stopped VM (`source_vm_id`); `firmware` `bios`/`efi`. Apply waits for `READY`
    (`timeouts.create`, default 60 minutes); a failed import fails the apply with the platform message and the image is
    replaced next time. `image` (`custom:<id>`) goes into `h3_vm.image`. Data source **h3_vm_image** by id or name.
    Custom images are PREVIEW; storage is billed 12 ₽/GiB·month of `size_gi` (no free volume), 10 images per project up
    to 256 GiB each, creation needs a positive workspace balance; the resource page shows the upload from your computer
    through your own S3 bucket and a presigned URL.
  - Import of both new resources by `<project_id>/<id>`, or by `<id>` when the provider has a default `project_id`.
  - These need an h3vm with the features enabled for the project; until then the API answers `404 *_DISABLED`
    (`400` for the `h3_vm` arguments).
- **h3_database_pooler**: PgBouncer poolers of a PostgreSQL database, several per database (h3db multi-pooler,
  `/api/databases/v1/{id}/poolers`). `pooler_id` and `role` (`rw`, `ro`, `r`) replace the pooler; `display_name`,
  `mode`, `instances`, `default`, `paused` (`pause`/`resume`), `parameters` and the listeners `vpc_mode`/`vpc_port`
  (`port` on the VPC address of the database, an own VPC `address`, or `none`) and `public_port`/`public_via` (a port on
  the public EIP of the database, `0` = none, `5432` = instead of PostgreSQL) change in place: the pods roll,
  PostgreSQL is not restarted. `parameters` is authoritative over the PgBouncer allowlist: a removed key returns to the
  default; the plan checks keys and value format, h3db checks ranges and the connection budget. Apply waits for
  `READY` (`PAUSED`) and an assigned VPC listener. Computed: `k8s_name`, `phase`, `ready_instances`,
  `effective_parameters`, `adopted`, `vpc_ip`, `vpc_status`, `vpc_endpoint`, `public_addresses`, `public_endpoints`.
  Import `<database_id>/<pooler_id>`.
- Data source **h3_database_poolers**: the poolers of a database with the connection `budget` and the h3db `limits`
  (`max_poolers`, `max_instances`, ports).
- h3db pooler errors (`POOLER_MULTI_DISABLED`, `POOLER_EXISTS`, `POOLER_IN_USE`, `POOLER_LIMIT_REACHED`,
  `ROLE_NEEDS_REPLICA`, `PORT_IN_USE`, `PORT_OUT_OF_RANGE`, `PUBLIC_NEEDS_EIP`, `VPC_ADDRESS_UNAVAILABLE`,
  `INVALID_LIMITS` and others) are shown with the code and a readable text; a 404 `POOLER_MULTI_DISABLED` never drops
  the pooler from the state. Needs h3db with the multi-pooler API (pending).

- **h3_vpc_route**: route table of a VPC — destination → `vm` (VPN/firewall VM of the VPC), `ip` or `blackhole`;
  import `<vpc_id>/<route_id>`. `h3_vpc.static_routes` is deprecated: the API refuses static routes at VPC creation.

- **h3_k8s_cluster `maintenance_window`:** `auto_upgrade` (`minor` default, `patch` = only patch versions, `none`) and
  `exclusions` (periods without automatic upgrades, UTC dates). Needs h3k8s with !33 (pub h3k8s 421 has it); older h3k8s ignores both.

- **h3_database: PostgreSQL read replicas** (h3db TRD 17). `replica_of` (the source id) creates the database with
  `POST /api/databases/v1/{replica_of}/replicas`; another id replaces the replica. `version`, `subnet_name` and a
  non-postgres `engine` together with `replica_of` are a plan error. When the source exists, the plan checks the size
  and `max_connections` (not below the source: `REPLICA_SIZE_TOO_SMALL`, `REPLICA_MAX_CONNECTIONS_TOO_LOW`), the
  project and that the source is not a replica itself (`SOURCE_IS_REPLICA`). The block `replica_settings`
  (`max_standby_streaming_delay_seconds`, `max_standby_archive_delay_seconds` 0-3600, default 900;
  `hot_standby_feedback`, default false) changes in place with `PATCH /settings`, without a restart. `promote = true`
  makes the replica a standalone database (`POST /promote`, backups on); this cannot be undone. After the promotion
  the plan is empty even if `replica_of` and `promote` stay in the configuration. Removing `replica_of` from a replica
  without `promote = true` is a plan error: the replica is neither promoted nor replaced. New computed attributes:
  `replication_state`, `replication_lag_seconds`, `promoted_from`. Import of a replica fills `replica_of`. A replica
  growing together with its source is not a change.
- **h3_database_user** with the `database_id` of a replica is a plan error `REPLICA_READ_ONLY`: users of a replica are
  created on its source.
- **h3db replica errors** (`READ_REPLICAS_DISABLED`, `SOURCE_BACKUPS_REQUIRED`, `REPLICA_LIMIT_REACHED`,
  `SOURCE_HAS_REPLICAS`, `REPLICA_READ_ONLY`, `CHANGE_IN_PROGRESS`, `DATABASE_NOT_RUNNING` and others) are shown with
  the code and a readable text. Deleting a source with replicas lists its replicas in the error.
- **h3_k8s_cluster `network_id`** (optional, replaces the cluster): the worker nodes run in that network of the
  project VPC and reach its databases and VMs by private addresses. Omitted = the platform default (the project
  network `default` where workers in the VPC are enabled, otherwise the shared platform network). Computed
  `network { mode, network_id }` shows where the workers live; the data source `h3_k8s_cluster` has `network_mode`
  and `network_id`. Needs h3k8s with workers in the tenant VPC (K18); older platforms leave `network` null.
- **h3_lb:** `white_ip` (default `true`) and computed `vpc_ip`. `white_ip = false` creates an internal load
  balancer: an address inside the VPC of its targets, reachable only from that VPC, no EIP. `white_ip` changes in
  place (on: `eip_id` or a new EIP; off: the EIP is detached and stays in the project). Needs h3lb with
  `H3LB_INTERNAL_LB_ENABLED`.
- **h3_database `storage_autoscale`** (optional, computed nested attribute `storage_autoscale = { ... }`): automatic
  disk growth by h3db (`enabled`, `max_size_gb` <= 1024, `threshold_percent` 60-95, `step_gb` 5-1024, `step_percent`
  0 or 5-100; computed `at_max`, `state`). Not set: h3db applies its default to a new database (enabled,
  `max_size_gb` = 2 × `size`, owner decision 2026-10-20), the state shows the platform values, nothing is sent and a
  console change is no diff. Set: managed; `enabled` is required, omitted values keep the current ones, `enabled =
  true` without `max_size_gb` and none on the platform plans 2 × size; sent on create as `storageAutoscale`
  (`enabled = false` too, overriding the default), changed in place with `PUT /api/databases/v1/{id}/storage-autoscale`.
  Removing it from the configuration keeps the setting; turn it off with `enabled = false`. A disk grown by autoscale
  is not a diff: `size` keeps the configured value, the new computed `current_size` shows the real size, a `size` not
  above it sends no resize. Needs h3db with storage autoscale (`503 STORAGE_AUTOSCALE_DISABLED` otherwise:
  `enabled = true` fails on apply, reads keep the configured value).
- **h3_eip `description`** (optional): a one-line comment shown in the console next to the address (up to 255
  characters, no control characters, no leading/trailing spaces). Changed and removed **in place** with
  `PATCH /api/ovn/v1/eips/{id}` (h3ovn !28), the EIP and its address are kept. Also in the data sources `h3_eip` and
  `h3_eips` (`eips[*].description`). Platforms without the field return no comment; setting it there fails on apply.
- **h3_vm `ssh_key_ids`** (set): your h3ssh keys on the VM (your own grant, mode `selected`; `[]` revokes it), changed
  **in place** with `PATCH /api/vms/v1/{id}` without a restart (0.2 replaced the VM with its root disk on any key
  change). Read from `GET /api/vms/v1/{id}/ssh-access`: the grant the provider created or changed, or after an import
  the grant made at VM creation. One key is sent as `ssh_key_id` and works on platforms without SSH access
  management; two or more keys need it (`503 SSH_ACCESS_DISABLED` otherwise).
- **h3_vm `ssh_access_managed`**: the platform keeps `/home/user/.ssh/authorized_keys` in sync with the keys and
  grants through qemu-guest-agent (default on for new VMs on images with the agent; read from the ssh-access view, null
  where the platform does not offer it). **Changing it on a running VM restarts the VM**; keys added to
  `authorized_keys` by hand are removed, keep your own in `authorized_keys2`.
- **h3_vm_ssh_access:** SSH access of a project member to a VM (h3vm `PUT/DELETE /api/vms/v1/{id}/ssh-access/grants/{user_id}`):
  `mode = "all"` (every current and future key of the member) or `"selected"` with `key_ids`; computed `active`,
  `inactive_reason`, `granted_by`, `granted_at`. Applied live, revoked on destroy (`allow_empty=true`, also when it
  was the last key). Import `<vm_id>/<user_id>`. PROJ-VIEWER members cannot get access (`422 SSH_GRANTEE_NOT_MEMBER`).
  Contract: OpenAPI of h3vm `feature/sshkeys-vm-access` (`publicapi/openapi/openapi.yaml`).
- **Client:** a `503` whose body carries a `*_DISABLED` code (a platform feature flag that is off) is no longer
  retried.
- **h3_database.max_connections** (PostgreSQL): 20-5000, at most 100 per Gi of memory; changed in place with a restart
  of PostgreSQL (`PUT /settings`), apply waits until it runs with the new value. Read back for existing databases.
- **h3_database_user**: a user of an `h3_database` with `connection_limit` (-1 or 1-5000, changed in place without a
  restart) and `grants` (owner / readwrite / readonly per database). Import `<database_id>/<name>`.
- **Provider:** `project_id` (env `H3_PROJECT_ID`), the default project. `project_id` on every project-scoped resource
  and data source is now optional: the default is used on create and then kept in state, so changing the provider
  default never moves or replaces existing resources. An explicit different `project_id` still replaces the resource.
- **h3_k8s_cluster** (managed Kubernetes, h3k8s `/api/k8s/v3`): `kubernetes_version` is **upgraded in place**
  (`POST /upgrade`, one step: newer patch or next minor; downgrade refused at plan time; never a replacement);
  `control_plane_replicas`, `workers.replicas`/`cpu`/`memory_gi`, `autoscaler` and `maintenance_window` change in
  place; autoscaler-driven replica changes are not drift; `kubeconfig` (sensitive), `endpoint` and
  `cluster_ca_certificate` for the kubernetes/helm providers. Import `<project_id>/<cluster_id>`. Data sources
  **h3_k8s_versions** (platform versions, `upgrade_targets` from a version), **h3_k8s_cluster**,
  **h3_k8s_kubeconfig**. `workers` is the node pool `default` of the cluster; more pools with `h3_k8s_node_pool`.
  Uses API-key access to h3k8s (`api.h3llo.cloud/api/k8s/v3`, h3k8s !37): the key must list the project; the
  kubeconfig (admin credentials) needs scope `k8s:write`. `api_allowed_cidrs` (h3k8s !36): source networks allowed
  to reach the Kubernetes API, changed in place (`PUT /api-access`, no restart); `[]` opens the API, omitted keeps
  the current list; also exported by the `h3_k8s_cluster` data source.
- **h3_k8s_node_pool** (h3k8s `/api/k8s/v3/clusters/{id}/node-pools`, h3k8s MR !39): additional worker pools of a
  cluster with their own `cpu`/`memory_gi`/`disk_gi`, `disk_profile` (`replicated`, `local` where offered),
  `replicas`, `autoscaling` (`min_size` from 0, `max_size`, `scale_down_unneeded_time`,
  `scale_down_utilization_threshold`, `max_node_provision_time`), `labels`, `taint` blocks, `max_pods` and `kubelet`
  reservations (`platform_defaults`, `system_reserved`, `kube_reserved`, `eviction_hard`). `replicas`, `autoscaling`,
  `labels` and `taint` change in place without a rollout; `cpu`, `memory_gi`, `disk_gi`, `max_pods` and `kubelet`
  change in place with a rolling replacement of the pool nodes (never a replacement of the resource); `name`,
  `cluster_id` and `disk_profile` replace the pool. With autoscaling the autoscaler owns the node count: `replicas`
  is not configured (it shows `min_size`), `current_replicas` shows the actual count, no drift.
  `kubernetes_version` (optional) upgrades the pool in place (`POST .../upgrade`, up to the control plane version;
  downgrade refused at plan time); a cluster upgrade moves all pools. 409 (cluster or pool busy) is retried after
  the pool is `READY`; delete is asynchronous (waits for 404). Computed `status`, `current_replicas`,
  `billed_replicas`. Import `<cluster_id>/<name>` (provider default project) or `<project_id>/<cluster_id>/<name>`.
  Data source **h3_k8s_node_pools** (all pools of a cluster with their state, `default` included).
- **h3_k8s_cluster `autoscaler.expander`** (`random`, `least-waste`, `most-pods`) and
  **`autoscaler.balance_similar_node_groups`**: cluster-wide cluster-autoscaler settings (`PATCH /autoscaler`), sent
  only when set; effective on clusters with node pools. h3k8s does not return them yet, so changes outside Terraform
  are not detected.
- **h3_k8s_cluster `node_local_dns_cache`** (bool, default `false`): NodeLocal DNSCache, a caching DNS server on every
  node; DNS queries of pods to kube-dns are redirected to the local cache with a CiliumLocalRedirectPolicy
  (`hostNetwork` pods and the nodes bypass it). Sent on create only when `true` (`nodeLocalDNSCache.enabled`), changed
  **in place** with `PATCH /api/k8s/v3/clusters/{id}/node-local-dns-cache`; the provider waits for the cache state
  `READY` / `DISABLED` (state `ERROR` fails the apply with the platform message). The first enablement on an older
  cluster does a rolling restart of the Cilium agents. Refusals are shown with their reason:
  `NODE_LOCAL_DNS_FEATURE_DISABLED`, `NODE_LOCAL_DNS_UNSUPPORTED` (422), `NODE_LOCAL_DNS_CONFLICT` (409, own
  node-local-dns or redirect policy for kube-dns in the cluster; not retried), `NODE_LOCAL_DNS_BUSY` (409). A backend
  without the feature reads as `false`. Data source **h3_k8s_cluster**: `node_local_dns_cache`,
  `node_local_dns_cache_state`.
- **h3_lb** (load balancer, h3lb `/api/loadbalancers/v1`): listeners (`port` -> `target_port`, TCP), private-IP
  `targets`, `round_robin`; computed `external_ip`/`endpoint`. Listener/target changes replace the LB. `eip_id`
  (LB Basic, OVN-native projects): publish on a free project Elastic IP, or export the newly allocated one; changing
  it moves the LB to the new EIP **in place** (h3lb PATCH); 409 `EIP_ALREADY_BOUND` is reported like for h3_vm.
  `release_eip_on_destroy` (default false): destroying the LB keeps its EIP in the project unless set. UDP is still
  refused at plan time. Import `<project_id>/<id>`. Data sources **h3_lb** (by id or name) and **h3_lbs**. **Requires API-key access to h3lb, which
  is not published on api.h3llo.cloud yet.**
- **h3_vm_security_group_attachment:** one security group on one VM, non-exclusive (other groups stay). Uses the point
  attach/detach API of the network service (h3ovn MR !14) with retries on `409 REVISION_CONFLICT`; where the public
  API does not serve it yet, falls back to read-modify-write of the VM list, serialized per VM within one apply.
  Import `<vm_id>/<security_group_id>`.
- **h3_dns_nameservers** data source (`GET /api/dns/v1/nameservers`): the h3llo DNS name servers for delegation.
- **Data sources h3_vpc, h3_network, h3_eip** (lookup by id, or by name in a project; EIP also by `ip_address`) and
  **h3_eips** (EIPs of a project, `bound = false` for the free pool: the console EIP selector for `eip_id`).
- **h3_database:** `eip_id` selects an existing free project Elastic IP as the public address (requires
  `white_ip = true`; sent as `eipId`, read back from `netConfig.eipId`). Changing it is in place (detach, then attach
  the selected EIP); `white_ip` true/false keeps working in place and attaches the selected `eip_id` when set.
- **h3_database:** `release_eip_on_destroy` (default `false`) deletes the EIP with the database (`releaseEip=true`).
- Clear diagnostics for `409 EIP_ALREADY_BOUND` ("Elastic IP already in use") and `403 EIP_DENIED` (EIP quota or
  balance) on `h3_vm` and `h3_database`.

### Changed

- **h3_dns_recordset:** writes are conditional on the record set version (ETag). Create sends `If-None-Match: *`
  (never overwrites a record set made elsewhere unless `allow_overwrite = true`; replaces the racy GET-then-PUT);
  update and delete send `If-Match` with the version read at refresh, so a change made in the console between plan
  and apply is reported (`412`) instead of overwritten. A retried PUT that already went through is recognized and
  not reported as a conflict. Platform-managed record sets (`409 MANAGED_RECORDSET`) get a clear message.
- **h3_dns_zone:** the PATCH carries only the changed fields (an unchanged `dnssec` no longer triggers a signing
  reconcile); removing all `labels` now clears them (0.2 sent nothing). Turning DNSSEC off while the parent still
  publishes DS gets an explanatory error.
- **h3_vm_security_groups:** `security_group_ids = []` is allowed and detaches every group (platform decision:
  a detached VM is not put back on `default`). New `default_on_destroy` (default `true`) keeps the 0.2 destroy
  behaviour; the project of a detached VM is found through the VM API or the provider `project_id`.
- **h3_dns_zone (data), h3_security_group (data):** `project_id` defaults to the provider `project_id`.
- **h3_vm:** `cpu`/`memory` changes use h3vm live resize when the platform has it: an increase within the reserve
  of the running VM (new VMs start with 4x their vCPU and memory) is applied **without a restart**, and the apply
  waits until the guest has it (`resize.state = applied`, up to 5 minutes, then a warning) so the next plan is
  empty; a decrease or any other change restarts the VM as before. New `allow_restart_on_resize` (default `true`,
  sent as `allow_restart`): `false` never restarts the VM: a change that needs a restart fails with a clear
  `RESIZE_RESTART_REQUIRED` diagnostic (reason included) and nothing is changed; on a platform without live resize
  `false` is refused before the PATCH. `409 RESIZE_BUSY` (live migration or another change in progress) is retried
  with a backoff (5 attempts). Platforms without live resize keep the previous behaviour (always a restart).
- **h3_vm:** `white_ip` is changed **in place** instead of replacing the VM (0.2 destroyed and recreated the VM, with
  its root disk, to toggle the public IP). `true` attaches `eip_id` or a newly leased project EIP through the network
  API; `false` detaches the current EIP, which stays in the project unless `release_eip_on_destroy = true`.
- **h3_database:** on platforms with the network service, `white_ip = true` without `eip_id` leases a new, billed
  project EIP, and destroying the database keeps that EIP in the project (billed) unless `release_eip_on_destroy` is
  set.

### Fixed

- Release binaries are built with Go 1.26.11 (GO-2026-6859 in `net/http`).

## [0.2.0] - 2026-10-07

Upgrade notes for 0.1.x users are at the end of this section. Run `terraform plan` after upgrading and read it before
`apply`: the first refresh fills arguments that 0.1.x never read back, so a plan may show differences that already
existed between your configuration and the cloud.

### Breaking changes

- **Provider:** `api_endpoint` defaults to `https://api.h3llo.cloud` (0.1.x defaulted to `http://127.0.0.1:4001`, so
  every configuration had to set it). `max_retries = 0` now disables retries (0.1.x turned 0 into 3).
- **h3_vm:** changing `disk_size`, `image`, `subnet_name` or `white_ip` now plans an explicit **replacement** of the VM
  (0.1.x planned an in-place update that failed with "Update not supported"). Add `lifecycle { prevent_destroy = true }`
  to VMs that must never be recreated.
- **h3_ssh_key:** renaming a key or changing `public_key` replaces it; the public API has no update (0.1.x called a
  `PATCH` that does not exist). `public_key` is no longer marked sensitive, so its diff is visible.
- **h3_s3_bucket:** `id` is `<project_id>/<name>` (was the empty or internal bucket id). Import uses the same format.
- **h3_snapshot, h3_backup:** import ID is `<project_id>/<id>` (the API needs the project to read them).
- **h3_backup:** create waits until the backup is `READY` (up to 60 minutes) instead of returning while it uploads.
- **h3_vm:** destroying a VM keeps its Elastic IP in the project unless `release_eip_on_destroy = true`
  (the current platform deleted it by default).

### Added

- **h3_database** (PostgreSQL, MariaDB): create, in-place storage increase (online), in-place `cpu`/`memory` change
  (restart), public access switch, credentials, import.
- **h3_vm_images** data source: the image catalog with minimum disk/memory/CPU and status.
- **h3_vm:** `eip_id` selects an existing free Elastic IP on create and moves the public IP in place;
  `release_eip_on_destroy`; computed `internal_ip`, `subnet_id`, `vpc_id`. `eip_id` needs a platform version with EIP
  selection.
- **h3_disk:** `performance_class` (QoS class, e.g. `R_STANDARD`, `R_FAST`), changed in place.
- **h3_ovn_network:** `id` (same as `subnet_id`) and `gateway_ip`.
- **Import** for every resource, with every argument the API returns filled in.
- **Provider:** `H3_TIMEOUT`, `H3_MAX_RETRIES`; User-Agent `terraform-provider-h3/<version>`.
- **Registry/OpenTofu:** `terraform-registry-manifest.json` (protocol 6.0) is shipped with releases.
- **h3_security_group:** Project security groups (stateful firewall on the VM port). Inline `rule` blocks own the whole rule set; without them the rules are left to `h3_security_group_rule`. Server-side defaults and canonical CIDRs do not cause diffs; updates use the group revision and retry on `REVISION_CONFLICT`. The project `default` group can be imported and edited; destroying it only removes it from state.
- **h3_security_group_rule:** One rule managed separately (import `<sg_id>/<rule_id>`); parallel applies on one group are safe.
- **h3_vm_security_groups:** Exclusive list of 1..5 groups of a VM, applied live; destroy puts the VM back on the project `default` group.
- **Data source h3_security_group:** Lookup by `id` or `project_id` + `name` (including `default`), with rules.
- **h3_dns_zone**, **h3_dns_recordset**, **h3_dns_ptr** resources and the **h3_dns_zone** data source (h3llo DNS):
  zones with ownership verification and DNSSEC (DS output), whole-RRset record sets (import `<zone_id>/<name>/<type>`),
  forward-confirmed PTR of Elastic IPs. They need the DNS API (`/api/dns/v1`), which is being rolled out; where it is
  not available yet the API answers 404.

### Changed

- **h3_database:** `cpu` and `memory` are changed in place (up or down) instead of replacing the database. The database restarts (PostgreSQL single instance: new connections refused ~1-4 min, smart shutdown waits up to 180 s for open sessions; MariaDB ~1 min). Apply waits up to 20 minutes for `RUNNING` with `compute_status` = `READY`; a 409 (resize or scale in progress) is retried after the running operation finishes. The cpu/memory shape (1-256 vCPU, 1-16 Gi per vCPU) is checked at validate time. New read-only attribute `compute_status`.

### Fixed

- **h3_disk:** changing `size` no longer fails with "Provider returned invalid result object after apply", which tainted
  the disk so that the next apply **deleted it with its data**. The size grows in place; shrinking is refused at plan
  time. Create no longer fails on `attached_to_vm_id`.
- **h3_vm:** a VM without `disk_size` no longer becomes tainted (and replaced by the next apply); `disk_size` and `image`
  are read from the API. Changing `cpu` on a VM with `white_ip = true` no longer fails with an inconsistent `white_ip`.
  `memory = "4Gi"` and `"4096Mi"` are the same value.
- **h3_ovn_vpc / h3_ovn_network:** creating a VPC without `namespaces` or a network without `external_subnets` no longer
  fails with "Value Conversion Error". The network reads the real h3ovn response (it lost the network id before).
- **Drift:** `Read` refreshes every argument from the API (CPU, memory, size, names, ...), so changes made in the
  console show up in `terraform plan`.
- **Import:** imported resources no longer plan a replacement (0.1.x stored only `id` and `status`).
- **Resources that fail to become ready** stay in state as tainted instead of being forgotten while still billed.
- **Retries:** a create (POST) is retried only on 429/502/503/504 or when the connection was not established, honoring
  `Retry-After`; a timeout or 500 is not retried blindly (0.1.x could create a duplicate VM or database). Every retry is
  signed again and resends the body (0.1.2 sent an empty body with the old signature after a 5xx).
- **Signing:** query values with spaces, `&` or `=` are encoded and signed correctly.
- **h3_s3_bucket:** import and refresh work (a missing bucket is planned for re-creation instead of failing).

### Upgrade from 0.1.x

1. Set `version = "~> 0.2"`, run `terraform init -upgrade`, then `terraform plan`.
2. Remove `api_endpoint = "https://api.h3llo.cloud"` if you set it only because of the old default (optional).
3. If the plan shows `h3_vm` replacement for `white_ip`, `disk_size` or `image`, your configuration differs from the
   VM; align the configuration with the cloud (or accept the replacement deliberately).
4. If the plan replaces `h3_ssh_key` because of `name`, restore the old name in the configuration; VMs are not affected
   by the replacement of a key.
5. `h3_disk` with a configured `size` smaller than the real disk is now refused at plan time: set the real size.
6. No state migration is needed: the schema version is unchanged, new attributes are filled on the first refresh.

## [0.1.2] - 2026-05-30

### Fixed

- **h3_backup / h3_snapshot:** Pass `project_id` query parameter on DELETE (required by the disks API).
- **h3_disk:** Set `attached_to_vm_id` in state after create; wait until disk status is `AVAILABLE` or `ATTACHED`.
- **h3_snapshot:** Wait until snapshot status is `READY` before completing create (unblocks `h3_backup`).
- **h3_vm:** Wait up to 15 minutes for `RUNNING`; when `white_ip` is true, wait until `endpoint` is populated; treat `FAILED` and cancel distinctly from timeout; poll every 5s with an immediate first check (was 10s, first check delayed).
- **h3_backup:** Surface HTTP 409 when snapshot is not ready.

## [0.1.1] - 2026-02-27

### Added

- **Provider:** HMAC authentication with `key_id` and `secret_key`, configurable endpoint, timeout, and retry settings. Supports `H3_API_ENDPOINT`, `H3_KEY_ID`, `H3_SECRET_KEY` environment variables.
- **h3_vm:** Create, read, update, and delete virtual machines. Supports CPU, memory, disk size, OS image selection, SSH keys, VPC subnet placement, and public IP assignment. Create VMs from snapshots or backups.
- **h3_disk:** Manage block storage volumes with configurable size and storage class.
- **h3_snapshot:** Create and manage disk snapshots for point-in-time recovery.
- **h3_backup:** Create and manage off-site backups of disk snapshots.
- **h3_ovn_vpc:** Create and manage Virtual Private Clouds with static routing.
- **h3_ovn_network:** Create and manage subnets within a VPC.
- **h3_ovn_eip:** Allocate and manage Elastic IP addresses.
- **h3_s3_bucket:** Create S3-compatible object storage buckets. Returns access credentials on creation.
- **h3_ssh_key:** Upload and manage SSH public keys for VM access.

[0.2.0]: https://github.com/h3llo-cloud/terraform-provider-h3/releases/tag/v0.2.0
[0.1.2]: https://github.com/h3llo-cloud/terraform-provider-h3/releases/tag/v0.1.2
[0.1.1]: https://github.com/h3llo-cloud/terraform-provider-h3/releases/tag/v0.1.1
