---
page_title: "End-to-End Self-Managed VMware Engine Node Preparation"
description: |-
  A comprehensive walkthrough for deploying Bare Metal Compute Engine instances and configuring Self-Managed VMware Engine prepared nodes with VCF Installer access through Terraform.
---

# End-to-End Self-Managed VMware Engine Node Preparation

This walkthrough demonstrates how to orchestrate an end-to-end deployment of Self-Managed VMware Engine (SMVE) prepared nodes on Bare Metal Compute Engine infrastructure using Terraform.

## Architecture Overview

Deploying VMware Cloud Foundation (VCF) on Self-Managed VMware Engine requires orchestrating resources across three distinct layers:
1. **Compute Engine Layer**: Provisioning the underlying Bare Metal instances, subnetworks, and placement resource policies.
2. **Networking Setup (VCF Load Balancer)**: Configuring an internal managed load balancer (Network Endpoint Group, Backend Service, and Forwarding Rule) to establish secure management connectivity for the VCF Installer.
3. **Self-Managed VMware Engine API**: Invoking the `:prepareNode` custom API resource (`google_self_managed_vmware_engine_prepared_node`) to configure ESXi host passwords, deploy VCF Installer components, and initiate configuration drift monitoring.

---

## Step 1: Define User Inputs & Variables

Configure standard GCP project variables along with bare metal configuration and sensitive appliance credentials.

```hcl
variable "project_id" {
  description = "Standard GCP project ID."
  type        = string
}

variable "region" {
  description = "Standard GCP region."
  type        = string
}

variable "zone" {
  description = "Standard GCP zone."
  type        = string
}

variable "node_count" {
  description = "Number of GCE Bare Metal instances."
  type        = number
  default     = 1
}

variable "machine_type" {
  description = "The specific GCE bare metal machine type (e.g., z3-highmem-192-metal)."
  type        = string
  default     = "z3-highmem-192-metal"
}

variable "esxi_image_family" {
  description = "Specifies the source ESXi image family."
  type        = string
  default     = ""
}

variable "esxi_image_project" {
  description = "Specifies the source ESXi image project."
  type        = string
}

variable "vpc_network_name" {
  description = "The existing VPC network name."
  type        = string
}

variable "esxi_root_password" {
  description = "Root password for each ESXi host."
  type        = string
  sensitive   = true
}

variable "vcf_installer_metadata" {
  description = "An object containing details crucial for VCF deployment."
  type = object({
    appliance_fqdn          = string
    vcf_version             = string
    appliance_root_password = string
    local_user_password     = string
  })
  sensitive = true
}

variable "subnet_name" {
  description = "Optional existing subnet name. If not provided, a new subnet is created."
  type        = string
  default     = ""
}

variable "resource_policy_name" {
  description = "Optional existing resource policy name. If not provided, a new placement policy is created."
  type        = string
  default     = ""
}

variable "neg_name" {
  description = "Optional existing NEG name. If not provided, a new NEG is created."
  type        = string
  default     = ""
}

variable "vcf_lb_ip_type" {
  description = "Determines if a new IP is allocated ('ephemeral') or an existing static IP is used ('static')."
  type        = string
  default     = "ephemeral"
}

variable "static_ip_name" {
  description = "The name of a pre-existing static IP address if vcf_lb_ip_type is 'static'."
  type        = string
  default     = ""
}
```

---

## Step 2: Compute Engine Infrastructure Setup

This phase sets up the underlying Bare Metal instances and attaches network interface cards (NICs) to either an existing or newly provisioned subnet. It also ensures instances adhere to placement policies.

```hcl
# Lookup existing subnet if subnet_name is provided
data "google_compute_subnetwork" "existing_subnet" {
  count  = var.subnet_name != "" ? 1 : 0
  name   = var.subnet_name
  region = var.region
}

# Create a new subnet if subnet_name is not provided
resource "google_compute_subnetwork" "new_subnet" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-prepared-node-subnet"
  ip_cidr_range = "10.0.1.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

# Lookup existing resource policy if provided
data "google_compute_resource_policy" "existing_policy" {
  count  = var.resource_policy_name != "" ? 1 : 0
  name   = var.resource_policy_name
  region = var.region
}

# Create a new placement policy if not provided
resource "google_compute_resource_policy" "new_policy" {
  count  = var.resource_policy_name == "" ? 1 : 0
  name   = "vcf-node-placement-policy"
  region = var.region

  group_placement_policy {
    vm_count = var.node_count
  }
}

locals {
  subnet_id          = var.subnet_name != "" ? data.google_compute_subnetwork.existing_subnet[0].id : google_compute_subnetwork.new_subnet[0].id
  resource_policy_id = var.resource_policy_name != "" ? data.google_compute_resource_policy.existing_policy[0].id : google_compute_resource_policy.new_policy[0].id
}

data "google_compute_image" "esxi_image" {
  family  = var.esxi_image_family != "" ? var.esxi_image_family : null
  project = var.esxi_image_project
}

resource "google_compute_disk" "bare_metal_disk" {
  count = var.node_count
  name  = "bare-metal-disk-${count.index}"
  zone  = var.zone
  type  = "hyperdisk-balanced"
  size  = 64
  image = data.google_compute_image.esxi_image.self_link

  guest_os_features {
    type = "IDPF"
  }
}

resource "google_compute_instance" "bare_metal_node" {
  count          = var.node_count
  name           = "bare-metal-node-${count.index}"
  zone           = var.zone
  machine_type   = var.machine_type
  can_ip_forward = true

  boot_disk {
    source      = google_compute_disk.bare_metal_disk[count.index].id
    auto_delete = true
  }

  network_interface {
    subnetwork = local.subnet_id
  }

  resource_policies = [local.resource_policy_id]

  scheduling {
    on_host_maintenance = "TERMINATE"
    automatic_restart   = true
  }

  network_performance_config {
    total_egress_bandwidth_tier = "TIER_1"
  }
}
```

---

## Step 3: Networking Setup (VCF Load Balancer)

Configure an internal managed load balancer targeting port `443`. This establishes the single management entry point required for deploying the VCF Installer.

```hcl
data "google_compute_network_endpoint_group" "existing_neg" {
  count = var.neg_name != "" ? 1 : 0
  name  = var.neg_name
  zone  = var.zone
}

resource "google_compute_network_endpoint_group" "new_neg" {
  count                 = var.neg_name == "" ? 1 : 0
  name                  = "vcf-installer-neg"
  network               = var.vpc_network_name
  subnetwork            = local.subnet_id
  default_port          = 443
  zone                  = var.zone
  network_endpoint_type = "GCE_VM_IP_PORT"
}

locals {
  neg_id   = var.neg_name != "" ? data.google_compute_network_endpoint_group.existing_neg[0].id : google_compute_network_endpoint_group.new_neg[0].id
  neg_name = var.neg_name != "" ? data.google_compute_network_endpoint_group.existing_neg[0].name : google_compute_network_endpoint_group.new_neg[0].name
}

resource "google_compute_network_endpoint" "node_endpoint" {
  count                  = var.node_count
  network_endpoint_group = local.neg_name
  zone                   = var.zone
  instance               = google_compute_instance.bare_metal_node[count.index].name
  port                   = 443
  ip_address             = google_compute_instance.bare_metal_node[count.index].network_interface[0].network_ip

  depends_on = [google_compute_instance.bare_metal_node]
}

data "google_compute_address" "static_ip" {
  count  = var.vcf_lb_ip_type == "static" ? 1 : 0
  name   = var.static_ip_name
  region = var.region
}

resource "google_compute_address" "ephemeral_ip" {
  count        = var.vcf_lb_ip_type == "ephemeral" ? 1 : 0
  name         = "vcf-lb-ip"
  subnetwork   = local.subnet_id
  address_type = "INTERNAL"
  region       = var.region
}

locals {
  lb_ip_address = var.vcf_lb_ip_type == "static" ? data.google_compute_address.static_ip[0].address : google_compute_address.ephemeral_ip[0].address
  lb_ip_id      = var.vcf_lb_ip_type == "static" ? data.google_compute_address.static_ip[0].id : google_compute_address.ephemeral_ip[0].id
}

resource "google_compute_region_health_check" "vcf_health_check" {
  name   = "vcf-installer-hc"
  region = var.region

  tcp_health_check {
    port = "443"
  }
}

resource "google_compute_region_backend_service" "vcf_backend_service" {
  name                  = "vcf-lb-backend"
  region                = var.region
  protocol              = "HTTPS"
  load_balancing_scheme = "INTERNAL_MANAGED"
  health_checks         = [google_compute_region_health_check.vcf_health_check.id]

  backend {
    group          = local.neg_id
    balancing_mode = "CONNECTION"
  }

  depends_on = [google_compute_network_endpoint.node_endpoint]
}

resource "google_compute_forwarding_rule" "vcf_forwarding_rule" {
  name                  = "vcf-lb-forwarding-rule"
  region                = var.region
  ip_protocol           = "TCP"
  ports                 = ["443"]
  load_balancing_scheme = "INTERNAL_MANAGED"
  backend_service       = google_compute_region_backend_service.vcf_backend_service.id
  subnetwork            = local.subnet_id
  ip_address            = local.lb_ip_id
  network               = var.vpc_network_name
}
```

---

## Step 4: Invoke Self-Managed VMware Engine API

The `:prepareNode` API depends on both the instances and load balancer setup being complete. Calling this API orchestrates setting the ESXi root password, deploying VCF Installer components via the load balancer IP, and triggering the Drift Manager.

```hcl
resource "google_self_managed_vmware_engine_prepared_node" "prepared_node" {
  count              = var.node_count
  location           = var.zone
  prepared_node_id   = google_compute_instance.bare_metal_node[count.index].instance_id
  esxi_root_password = var.esxi_root_password
  provider           = google-beta

  vcf_installer_metadata {
    appliance_fqdn          = var.vcf_installer_metadata.appliance_fqdn
    appliance_root_password = var.vcf_installer_metadata.appliance_root_password
    local_user_password     = var.vcf_installer_metadata.local_user_password
    forwarding_rule         = google_compute_forwarding_rule.vcf_forwarding_rule.id
    reserved_address        = local.lb_ip_id
  }

  depends_on = [
    google_compute_forwarding_rule.vcf_forwarding_rule,
    google_compute_network_endpoint.node_endpoint
  ]
}
```

---

## Step 5: Outputs & Verification

Expose instance IDs, prepared node IDs, and the VCF Load Balancer IP address.

```hcl
output "esxi_node_instance_ids" {
  description = "List of IDs for the created GCE instances."
  value       = google_compute_instance.bare_metal_node[*].instance_id
}

output "prepared_node_ids" {
  description = "List of IDs for the prepared nodes (synthetic IDs from the custom resource)."
  value       = google_self_managed_vmware_engine_prepared_node.prepared_node[*].id
}

output "vcf_load_balancer_ip" {
  description = "The internal IP address of the VCF Load Balancer, used to access the VCF Installer."
  value       = local.lb_ip_address
}
```

### Verification Checklist
- Run `terraform init` and `terraform plan`.
- Verify that `terraform plan` successfully resolves conditional subnet, resource policy, and NEG lookups.
- After running `terraform apply`, confirm via the Google Cloud Console that the Bare Metal instances are registered in the regional Backend Service health checks.
