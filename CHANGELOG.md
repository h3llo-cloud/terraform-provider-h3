# Changelog

All notable changes to the H3 Cloud Terraform Provider will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-02-27

### Added

- **Provider:** HMAC authentication with `key_id` and `secret_key`, configurable endpoint, timeout, and retry settings. Supports `H3_API_ENDPOINT`, `H3_KEY_ID`, `H3_SECRET_KEY` environment variables.
- **h3_vm:** Create, read, update, and delete virtual machines. Supports CPU, memory, disk size, OS image selection, SSH keys, VPC subnet placement, and public IP assignment. Create VMs from snapshots or backups.
- **h3_disk:** Manage block storage volumes with configurable size and storage class.
- **h3_snapshot:** Create and manage disk snapshots for point-in-time recovery.
- **h3_backup:** Create and manage VM backups.
- **h3_ovn_vpc:** Create and manage Virtual Private Clouds with static routing.
- **h3_ovn_network:** Create and manage subnets within a VPC.
- **h3_ovn_eip:** Allocate and manage Elastic IP addresses.
- **h3_s3_bucket:** Create S3-compatible object storage buckets. Returns access credentials on creation.
- **h3_ssh_key:** Upload and manage SSH public keys for VM access.

[0.1.0]: https://github.com/h3llo-cloud/terraform-provider-h3/releases/tag/v0.1.0
