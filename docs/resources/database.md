---
page_title: "h3_database Resource - h3llo cloud"
subcategory: ""
description: |-
  Manages a managed database (PostgreSQL or MariaDB) in h3db. size can be increased in place (online, no restart). cpu, memory and max_connections change in place, with a restart of the database. white_ip, eip_id and release_eip_on_destroy change in place; every other argument forces a new database. Storage cannot be decreased. With storage_autoscale the platform grows the disk itself; a disk larger than size is then not a diff. Destroying the database keeps its Elastic IP in the project (billed) unless release_eip_on_destroy is true. With replica_of the database is a PostgreSQL read replica of another database; promote = true makes it standalone.
---

# h3_database (Resource)

Manages a managed database (PostgreSQL or MariaDB).

`size` can be increased in place: the volume grows online, the database is not restarted, and the new size is billed from the moment the resize is accepted. Storage cannot be decreased; a smaller `size` is rejected at plan time.

`cpu` and `memory` change in place, up or down, within the same bounds as on create (1-256 vCPU, 1-1024 Gi, 1-16 Gi per vCPU). Changing them **restarts the database**:

- PostgreSQL, single instance (the only topology offered today): from the start of the change the server refuses new connections. PostgreSQL first waits up to 180 seconds for open sessions to finish (smart shutdown), then stops and starts again with the new resources. Expect new connections to fail for about 1-4 minutes; applications with connection pools that keep sessions open see the longer end.
- MariaDB, single replica: the server is restarted with the new resources, about 1 minute without connections.

Memory-dependent server settings (PostgreSQL `shared_buffers` / `effective_cache_size`, MariaDB `innodb_buffer_pool_size`) are recomputed for the new memory. The new cpu/memory is billed from the moment the change is accepted. Apply waits (up to 20 minutes) until the database is `RUNNING` again with `compute_status` = `READY`. If `size` changes in the same apply, the storage resize runs first; h3db refuses a cpu/memory change while a resize is in progress, and the provider waits for it to finish. A quota or role refusal from billing fails the apply without changing anything.

`max_connections` (PostgreSQL) changes in place and **restarts PostgreSQL**. Bounds: 20-5000 and at most 100 per Gi of `memory` (h3db refuses more with `INVALID_MAX_CONNECTIONS`). The restart is bounded: open sessions get up to 15 seconds (smart shutdown), then PostgreSQL stops and starts again in the same pod — with one instance expect about a minute without new connections; with replicas an increase ends with a switchover (seconds of reconnects), a decrease restarts the primary in place first. With a connection pooler the value must be at least pooler instances × `max_db_connections` + 10 (`409 POOLER_LIMITS`). Apply waits (up to 15 minutes) until PostgreSQL runs with the new value. On create a value above 1000 is applied right after the database starts (one restart of the empty database). Not set: the service default (100) is read back; removing it from the configuration keeps the current value.

## Storage autoscale

`storage_autoscale` lets h3db grow the disk by itself (all engines, same as a manual `size` increase). It is an optional, computed attribute:

```terraform
storage_autoscale = {
  enabled           = true
  max_size_gb       = 200 # >= size, <= 1024; not set: the current value, or 2 x size
  threshold_percent = 85  # 60-95
  step_gb           = 10  # 5-1024
  step_percent      = 20  # 0 (off) or 5-100
}
```

- **Not set** (the default): a new database gets the platform default, **enabled with `max_size_gb` = 2 × `size`** (at most 1024), threshold 85 %, step 10 GiB / 20 %. The state shows what h3db reports; the provider sends nothing and a change made in the console is not a diff. Existing databases keep their setting (off unless turned on).
- **Set:** the provider manages the values you write; `enabled` is required. Omitted values keep the current ones (85 / 10 / 20 on the first enable); `enabled = true` without `max_size_gb` on a database that has none plans 2 × the size. Changes apply in place (`PUT /api/databases/v1/{id}/storage-autoscale`, no restart, costs nothing by itself). On create, `enabled = false` is sent, so the new database starts without autoscale.
- **Removing it from the configuration keeps the current setting** (it does not turn autoscale off). To turn it off, set `enabled = false`.
- When the used space reaches `threshold_percent` (or the disk is full), h3db grows it by max(`step_gb`, `step_percent` of the current size, 5 GiB), and by enough to bring the usage below the threshold, but never above `max_size_gb`. At most one grow per 6 hours. It never shrinks.
- Each grow is a normal storage increase: online, without a restart, **billed from that moment**. The project role, quota and a positive balance are checked on every grow; a refusal is shown in `storage_autoscale.state` (`BLOCKED`). `at_max = true` means the disk reached `max_size_gb` and is still above the threshold: raise `max_size_gb` or `size`.
- On a platform where storage autoscale is not rolled out yet (`503 STORAGE_AUTOSCALE_DISABLED`), `enabled = true` fails the apply with a clear error; `enabled = false`, an omitted attribute (left null) and reads do not fail.

**Size drift.** After an automatic grow, h3db reports a larger size than the configured `size`. The provider treats that as expected and never plans a shrink or a replacement for it:

- `size` keeps the configured value in the state (the minimum size), `current_size` shows the real one. A plan right after a grow is empty.
- This holds while autoscale is on (also by the platform default) and after it is turned off, as long as h3db still reports an automatic grow in the recent events. A disk grown by hand (console, API) without autoscale is shown as before: plan time error "can only be increased" until `size` is raised to it.
- Raising `size` to a value that is not above `current_size` sends no resize (the disk is already that large) and only updates the state; a value above it is a normal resize.
- `max_size_gb` below `size` or below `current_size` is refused at plan time.

## Read replicas (PostgreSQL)

`replica_of` creates the database as a **read replica** of another PostgreSQL database of the same project (`POST /api/databases/v1/{replica_of}/replicas`). h3db fills the replica from the last backup of the source, then it follows the source by WAL streaming. The replica is read-only and has its own `cpu`, `memory`, `size`, public address (`white_ip`, `eip_id`), `storage_autoscale` and endpoint; its users, passwords and databases are those of the source.

```terraform
resource "h3_database" "analytics" {
  project_id = var.project_id
  name       = "main-prod-analytics"
  engine     = "postgres"
  cpu        = 8
  memory     = "32Gi"
  size       = "800Gi"                 # >= the size of the source
  replica_of = h3_database.prod.id     # replaces the replica when changed

  replica_settings {
    max_standby_streaming_delay_seconds = 900 # 0-3600, default 900
    max_standby_archive_delay_seconds   = 900 # 0-3600, default 900
    hot_standby_feedback                = false
  }

  # promote = true   # one-shot: make the replica a standalone database
}
```

- **Requirements of the source:** PostgreSQL, not a replica itself, backups enabled with WAL archiving in state OK and at least one completed base backup (`409 SOURCE_BACKUPS_REQUIRED`), at most 3 replicas (`409 REPLICA_LIMIT_REACHED`). A source created in the same apply has no backup yet: create the replica in a later apply. On platforms without the feature, create fails with `503 READ_REPLICAS_DISABLED`.
- **Plan-time checks:** with `replica_of`, `engine` must be `postgres`; `version` and `subnet_name` cannot be set (the replica takes the version and the network of the source; `version` is read back). When the source exists, the plan also refuses a `size` below the source's (`REPLICA_SIZE_TOO_SMALL`), a `max_connections` below the source's (`REPLICA_MAX_CONNECTIONS_TOO_LOW`), a source that is a replica (`SOURCE_IS_REPLICA`) and a `project_id` other than the source's. The same checks fail on apply with the API code when the source is created in the same apply.
- **Size:** when the source grows, h3db grows the replica with it. That is not a diff: `size` keeps the configured value, `current_size` shows the real one.
- **`replica_settings`** (only with `replica_of`): the standby delays and `hot_standby_feedback`. Values the block omits are planned as the defaults (900 / 900 / false). Changes apply in place with `PATCH /api/databases/v1/{id}/settings`, by a reload, without a restart. Without the block the platform defaults apply and the provider does not read or change them; removing the block keeps the current values. A query on the replica that blocks WAL replay is cancelled after the delay. `hot_standby_feedback = true` keeps such queries alive, but VACUUM on the source waits for them and its tables can grow.
- **Read-only:** users, grants, databases and backups of a replica are managed on the source. `h3_database_user` with the `database_id` of a replica is a plan-time error `REPLICA_READ_ONLY` (h3db also answers `409 REPLICA_READ_ONLY`).
- **State:** `replication_state` (`BOOTSTRAPPING`, `STREAMING`, `ARCHIVE`, `PAUSED`, `BROKEN`, `PROMOTING`, `UNKNOWN`) and `replication_lag_seconds` are refreshed on every read; they are null for a database that is not a replica. Import of a replica fills `replica_of`; `replica_settings` stays null after import until you add the block.
- **Deleting a source** that still has replicas fails with `409 SOURCE_HAS_REPLICAS` and the list of its replicas. In one configuration Terraform destroys the replica first, because it references the source.

### Promotion (`promote`)

`promote = true` on an existing replica makes it a standalone database with writes (`POST /api/databases/v1/{id}/promote` with `{"backup": {"enabled": true}}`, backups of the promoted database are on). **It is irreversible**: the replication stops, later changes of the source do not reach the database, and it cannot be attached to a source again. Apply waits (up to 15 minutes) until h3db no longer reports the database as a replica. If the wait fails, the state keeps the replica, and the next apply checks the promotion again.

- `promote` is a one-shot argument: it acts only when the database is a replica. On create (and on a replacement) it is a plan error. On a database that never was a replica it is a plan error (`NOT_A_REPLICA`).
- After the promotion `promoted_from` holds the former source, `replication_state` and `replication_lag_seconds` become null.
- **The configuration may stay as it is.** With `replica_of = <former source>` and `promote = true` still in the configuration the next plan is empty: `replica_of` in the state keeps the configured ID, which now names the former source (the same as `promoted_from`) and does not plan a replacement. Terraform requires the state to match a configured value, so `replica_of` is null in the state only when it is removed from the configuration.
- **Or clean it up.** Removing `replica_of`, `promote` and `replica_settings` after the promotion is an in-place update without API calls; nothing is re-created. You can also remove `replica_of` in the same apply that sets `promote = true`.
- `replica_settings` cannot change after the promotion (plan error), and `replica_of` set to another ID replaces the database with a new replica of that ID.
- **Removing `replica_of` from a replica** without `promote = true` is a plan error: "the database is a read replica of <id>; to make it standalone set promote = true; removing replica_of would re-create it". The provider never promotes or re-creates a replica because `replica_of` disappeared.

Users and their connection limits are managed with `h3_database_user`.

`white_ip`, `eip_id` and `release_eip_on_destroy` also change in place. Changing any other argument creates a new database, and the data is **not** copied.

## Public IP (Elastic IP)

- `white_ip = true` without `eip_id` leases a **new Elastic IP in the project, billed** as any project EIP (on platforms with the network service; older platforms used a non-pool address). Its ID is exported as `eip_id`.
- `white_ip = true` with `eip_id` uses that existing EIP. It must belong to the same project and be free (`h3_eip.bound = false`). An EIP attached to or reserved by another resource fails with `409 EIP_ALREADY_BOUND` ("Elastic IP already in use"); `eip_id` without `white_ip = true` is rejected at plan time.
- If the project may not lease another EIP (quota or balance), h3db answers `403 EIP_DENIED` and apply fails with "Elastic IP not allowed": select a free EIP with `eip_id`, release unused ones or top up.
- Changing `eip_id` moves the public address in place: the current EIP is detached (it stays in the project) and the selected one is attached. `white_ip` true -> false detaches the address; false -> true attaches it (the selected `eip_id`, or a newly leased EIP). Removing `eip_id` from the configuration keeps the current EIP.
- **Destroying the database keeps its EIP in the project by default**: it becomes a free EIP that can be selected again and **continues to be billed** until released. Set `release_eip_on_destroy = true` to delete it together with the database.

The password is stored in the Terraform state (`password`, sensitive): keep the state in an encrypted remote backend.

## Example Usage

```terraform
resource "h3_database" "pg" {
  project_id = var.project_id
  name       = "app-db"
  engine     = "postgres"
  cpu        = 2 # cpu/memory change in place, the database restarts
  memory     = "4Gi"
  size       = "20Gi" # increase in place, online

  # PostgreSQL only, 20..5000 and at most 100 per Gi of memory.
  # Changed in place: the database restarts (~1 min; with replicas a switchover).
  max_connections = 300

  # h3db grows the disk itself up to max_size_gb; the grown size is not a diff
  # (size stays the configured minimum, current_size shows the real one).
  # Optional: without it a new database gets autoscale on with max = 2 x size.
  storage_autoscale = {
    enabled     = true
    max_size_gb = 100
  }

  # Changing name, engine or version creates a new, empty database.
  lifecycle {
    prevent_destroy = true
  }
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `cpu` (Number) vCPU (1-256). Changed in place (up or down); the database restarts.
- `engine` (String) Engine: `postgres` or `mariadb`
- `memory` (String) Memory, Gi quantity (e.g. `4Gi`); 1-16 Gi per vCPU. Changed in place (up or down); the database restarts.
- `name` (String) Database name (lowercase letters, digits and '-', max 63)
- `size` (String) Storage size, Gi quantity (e.g. `20Gi`, max `1024Gi`). Can be increased in place; a smaller value is rejected at plan time. With `storage_autoscale` enabled this is the configured (minimum) size: the disk may be larger, see `current_size`.

### Optional

- `eip_id` (String) ID (UUID) of an existing, free Elastic IP of the project to use as the public IP. Requires `white_ip = true`. When omitted with `white_ip = true`, the ID of the leased EIP is exported here. Changing it moves the public address in place (the previous EIP stays in the project); removing it from the configuration keeps the current EIP.
- `max_connections` (Number) PostgreSQL `max_connections` (20-5000; h3db also caps it at 100 per Gi of `memory`). The service default when omitted. Changed in place; **the database restarts** (one instance: PostgreSQL restarts in place, about a minute without new connections; with replicas: a switchover, seconds). With a connection pooler it must be at least pooler instances × `max_db_connections` + 10.
- `project_id` (String) Project ID (UUID). Defaults to the provider `project_id` (`H3_PROJECT_ID`). Changing it replaces the database.
- `promote` (Boolean) One-shot action. Changing it to `true` on a read replica promotes it to a standalone database with writes (`POST /api/databases/v1/{id}/promote`, backups enabled). **Irreversible**: the replication stops and the database cannot be attached to a source again. Not allowed on create. After the promotion it has no effect and may stay in the configuration.
- `release_eip_on_destroy` (Boolean) Delete the database's Elastic IP together with the database (default: false). By default destroying the database keeps the EIP in the project, where it is billed until released.
- `replica_of` (String) ID of a PostgreSQL `h3_database` to create this database as its **read replica** (`POST /api/databases/v1/{replica_of}/replicas`). The version, project, network, users and databases come from the source; `version` and `subnet_name` cannot be set. `size` and `max_connections` cannot be below the source's. Changing it to another ID replaces the database. Removing it from the configuration of a replica is a plan error (set `promote = true` to make the replica standalone). After a promotion the value may stay in the configuration (it then names the former source, see `promoted_from`) or be removed; neither plans a replacement.
- `replica_settings` (Block, Optional) Settings of a read replica (only with `replica_of`). Changed in place with `PATCH /api/databases/v1/{id}/settings`, without a restart. Not set: the platform defaults (900 / 900 / false); removing the block keeps the current values. (see [below for nested schema](#nestedblock--replica_settings))
- `storage_autoscale` (Attributes) Automatic growth of `size`. When the used space reaches `threshold_percent`, h3db grows the disk by max(`step_gb`, `step_percent` of the size, 5 GiB) up to `max_size_gb` (at most once per 6 hours; each step is billed as a normal resize). **Not set:** a new database gets the platform default (enabled, `max_size_gb` = 2 × `size`, at most 1024) and the state shows the values h3db reports; the provider does not change them (a value set in the console is kept). **Set:** the provider manages it, changed in place (`PUT /storage-autoscale`, no restart). Removing it from the configuration keeps the current setting; to turn autoscale off set `enabled = false`. While autoscale is on, a disk that has grown beyond `size` is not a diff: `size` stays the configured value and `current_size` shows the real one. (see [below for nested schema](#nestedatt--storage_autoscale))
- `subnet_name` (String) Subnet to place the database in; the project default subnet when omitted
- `version` (String) Engine version (e.g. `17.5`, `11.8`); the service default when omitted
- `wait_for_resize` (Boolean) Wait (up to 15 min) until the volume has actually grown after a size increase. When false, apply returns once the resize is accepted (`storage_status` = `RESIZING`).
- `white_ip` (Boolean) Expose the database on a public IP. Changed in place. Without `eip_id` a new Elastic IP is leased in the project (billed).

### Read-Only

- `actual_size` (String) Current volume capacity reported by the cluster
- `compute_status` (String) `READY`, or `SCALING` while a cpu/memory change is being applied
- `current_size` (String) Storage size the database has now (h3db `sizeGb`). Larger than `size` after an automatic grow by `storage_autoscale`.
- `endpoint` (String) Public endpoint when `white_ip` is true
- `id` (String) Database ID
- `internal_ip` (String) Address inside the project network
- `password` (String, Sensitive) Client password
- `promoted_from` (String) ID of the former source when this database was a read replica and has been promoted.
- `replication_lag_seconds` (Number) Replication lag of a read replica in seconds, as h3db last measured it. Null when not a replica.
- `replication_state` (String) Replication state of a read replica: `BOOTSTRAPPING`, `STREAMING`, `ARCHIVE`, `PAUSED`, `BROKEN`, `PROMOTING` or `UNKNOWN`. Null for a database that is not a replica.
- `status` (String) Database status (`RUNNING`, `CREATING`, ...)
- `storage_status` (String) `READY`, or `RESIZING` while a size increase is in progress
- `username` (String) Client user

<a id="nestedblock--replica_settings"></a>
### Nested Schema for `replica_settings`

Optional:

- `hot_standby_feedback` (Boolean) `hot_standby_feedback` (default false). `true`: long queries on the replica are not cancelled, but VACUUM on the source waits for them and its tables can grow.
- `max_standby_archive_delay_seconds` (Number) `max_standby_archive_delay`, 0-3600 seconds (default 900): the same while WAL comes from the archive.
- `max_standby_streaming_delay_seconds` (Number) `max_standby_streaming_delay`, 0-3600 seconds (default 900): how long WAL replay waits for a conflicting query on the replica before the query is cancelled.


<a id="nestedatt--storage_autoscale"></a>
### Nested Schema for `storage_autoscale`

Optional:

- `enabled` (Boolean) Turn automatic storage growth on or off (required when `storage_autoscale` is set)
- `max_size_gb` (Number) Upper bound of automatic growth, GiB: at least the current size and at most 1024. Not set: the current value, or 2 × the size (at most 1024) when there is none.
- `step_gb` (Number) Minimum growth per step, GiB (5-1024; not set: the current value, 10 on the first enable)
- `step_percent` (Number) Growth per step, % of the current size, when larger than `step_gb` (0 = off, or 5-100; not set: the current value, 20 on the first enable)
- `threshold_percent` (Number) Used space, % of the disk, that triggers a grow (60-95; not set: the current value, 85 on the first enable)

Read-Only:

- `at_max` (Boolean) True when the disk is at `max_size_gb` and still above the threshold (autoscale cannot grow it any more)
- `state` (String) `DISABLED`, `WATCHING`, `GROWING`, `AT_MAX` or `BLOCKED` (empty when the platform does not report it)

## Import

```shell
# Import by ID
terraform import h3_database.example <id>
```
