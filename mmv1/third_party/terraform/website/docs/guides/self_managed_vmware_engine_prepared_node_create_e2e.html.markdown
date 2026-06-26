---
page_title: "End-to-End Self-Managed VMware Engine Node Preparation"
description: |-
  A comprehensive walkthrough for deploying Bare Metal Compute Engine instances, firewall rules, private DNS zones, multiple subnets, and configuring Self-Managed VMware Engine prepared nodes with Google Secret Manager integration via Terraform.
---

# End-to-End Self-Managed VMware Engine Node Preparation

This walkthrough demonstrates how to orchestrate an end-to-end deployment of Self-Managed VMware Engine (SMVE) prepared nodes on Bare Metal Compute Engine infrastructure using Terraform.

## Architecture Overview

Deploying VMware Cloud Foundation (VCF) on Self-Managed VMware Engine requires orchestrating resources across multiple distinct layers:
1. **Firewall Rules Layer**: Provisioning ingress firewall rules to manage network traffic within the VPC network.
2. **DNS Configuration Layer**: Setting up internal DNS policies, private forward/reverse DNS zones, and DNS records for local name resolution.
3. **Compute Engine Layer**: Provisioning the underlying Bare Metal instances, eight specialized subnetworks, and placement resource policies.
4. **Networking Setup (VCF Load Balancer)**: Configuring an internal managed load balancer (Network Endpoint Group, Backend Service, and Forwarding Rule) to establish secure management connectivity for the VCF Installer.
5. **Secret Management Layer**: Fetching host and appliance credentials securely from Google Secret Manager.
6. **Self-Managed VMware Engine API**: Invoking the `:prepareNode` custom API resource (`google_self_managed_vmware_engine_prepared_node`) to configure ESXi host passwords, deploy VCF Installer components, and initiate configuration drift monitoring.

---

## Step 1: Define User Inputs & Variables

Configure standard GCP project variables, network infrastructure options, and Secret Manager references for sensitive appliance credentials.

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

variable "esxi_root_password_secret_id" {
  description = "The Secret Manager secret ID containing the root password for each ESXi host."
  type        = string
}

variable "vcf_appliance_root_password_secret_id" {
  description = "The Secret Manager secret ID containing the root password for the VCF appliance."
  type        = string
}

variable "vcf_local_user_password_secret_id" {
  description = "The Secret Manager secret ID containing the local user password for the VCF appliance."
  type        = string
}

variable "license_key_secret_id" {
  description = "The Secret Manager secret ID containing the license key."
  type        = string
}

variable "vcf_installer_metadata" {
  description = "An object containing metadata crucial for VCF deployment."
  type = object({
    appliance_fqdn = string
    vcf_version    = string
  })
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

## Step 2: Firewall Rule Creation

Configure ingress firewall rules on the VPC to allow all necessary internal protocols.

```hcl
resource "google_compute_firewall" "allow_all" {
  name    = "gcve-vpc-network-allow-all"
  network = var.vpc_network_name

  allow {
    protocol = "all"
  }

  source_ranges = ["0.0.0.0/0"]
}
```

---

## Step 3: DNS Configuration

Create a DNS policy with inbound forwarding enabled, and establish private DNS zones (forward and reverse lookup zones) along with records for NTP, the VCF appliance, and ESXi nodes.

```hcl
resource "google_dns_policy" "dns_policy" {
  name                      = "vcf-dns-policy"
  enable_inbound_forwarding = true

  networks {
    network_url = var.vpc_network_name
  }
}

resource "google_dns_managed_zone" "forward_zone" {
  name        = "vcf-forward-zone"
  dns_name    = "gcve-vcf.test.gve."
  description = "Forward lookup zone for VCF"

  visibility = "private"

  private_visibility_config {
    networks {
      network_url = var.vpc_network_name
    }
  }
}

resource "google_dns_managed_zone" "reverse_zone" {
  name        = "vcf-reverse-zone"
  dns_name    = "0.10.in-addr.arpa."
  description = "Reverse lookup zone for VCF"

  visibility = "private"

  private_visibility_config {
    networks {
      network_url = var.vpc_network_name
    }
  }
}

resource "google_dns_record_set" "ntp" {
  name         = "ntp.gcve-vcf.test.gve."
  managed_zone = google_dns_managed_zone.forward_zone.name
  type         = "A"
  ttl          = 300
  rrdatas      = ["10.0.7.10"]
}

resource "google_dns_record_set" "appliance" {
  name         = "${var.vcf_installer_metadata.appliance_fqdn}."
  managed_zone = google_dns_managed_zone.forward_zone.name
  type         = "A"
  ttl          = 300
  rrdatas      = [local.lb_ip_address]
}

resource "google_dns_record_set" "esxi_nodes" {
  count        = var.node_count
  name         = "esxi-node-${count.index}.gcve-vcf.test.gve."
  managed_zone = google_dns_managed_zone.forward_zone.name
  type         = "A"
  ttl          = 300
  rrdatas      = [google_compute_instance.bare_metal_node[count.index].network_interface[0].network_ip]
}

resource "google_dns_record_set" "reverse_esxi_nodes" {
  count        = var.node_count
  name         = "${split(".", google_compute_instance.bare_metal_node[count.index].network_interface[0].network_ip)[3]}.${split(".", google_compute_instance.bare_metal_node[count.index].network_interface[0].network_ip)[2]}.0.10.in-addr.arpa."
  managed_zone = google_dns_managed_zone.reverse_zone.name
  type         = "PTR"
  ttl          = 300
  rrdatas      = ["esxi-node-${count.index}.gcve-vcf.test.gve."]
}
```

---

## Step 4: Compute Engine Layer

This phase sets up the underlying Bare Metal instances and attaches network interfaces. When creating a new network setup, we provision eight specialized subnets: ESXi, NSX TEP, VM Mgmt, vMotion, vSAN, Uplink, DNS, and Offline Depot.

```hcl
# Lookup existing subnet if subnet_name is provided
data "google_compute_subnetwork" "existing_subnet" {
  count  = var.subnet_name != "" ? 1 : 0
  name   = var.subnet_name
  region = var.region
}

# Create new subnets if subnet_name is not provided
resource "google_compute_subnetwork" "subnet_esxi" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-esxi"
  ip_cidr_range = "10.0.1.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_nsx_tep" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-nsx-tep"
  ip_cidr_range = "10.0.2.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_vm_mgmt" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-vm-mgmt"
  ip_cidr_range = "10.0.3.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_vmotion" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-vmotion"
  ip_cidr_range = "10.0.4.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_vsan" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-vsan"
  ip_cidr_range = "10.0.5.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_uplink" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-uplink"
  ip_cidr_range = "10.0.6.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_dns" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-dns"
  ip_cidr_range = "10.0.7.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

resource "google_compute_subnetwork" "subnet_offline_depot" {
  count         = var.subnet_name == "" ? 1 : 0
  name          = "vcf-subnet-offline-depot"
  ip_cidr_range = "10.0.8.0/24"
  region        = var.region
  network       = var.vpc_network_name
}

locals {
  subnet_id = var.subnet_name != "" ? data.google_compute_subnetwork.existing_subnet[0].id : google_compute_subnetwork.subnet_esxi[0].id
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

## Step 5: Networking Setup (VCF Load Balancer)

Configure an internal managed load balancer targeting port `443` on the ESXi instances. This establishes the management entry point required for deploying the VCF Installer.

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

## Step 6: Secret Management (Google Secret Manager)

To adhere to security best practices, retrieve all host and VCF installer appliance passwords from Google Secret Manager.

```hcl
data "google_secret_manager_secret_version" "esxi_root_pw" {
  secret = var.esxi_root_password_secret_id
}

data "google_secret_manager_secret_version" "vcf_appliance_root_pw" {
  secret = var.vcf_appliance_root_password_secret_id
}

data "google_secret_manager_secret_version" "vcf_local_user_pw" {
  secret = var.vcf_local_user_password_secret_id
}

data "google_secret_manager_secret_version" "license_key" {
  secret = var.license_key_secret_id
}
```

---

## Step 7: Invoke Self-Managed VMware Engine API

The `:prepareNode` API depends on both the instances and load balancer setup being complete. Calling this API orchestrates setting the ESXi root password, deploying VCF Installer components via the load balancer IP, and triggering the Drift Manager using the fetched secrets.

```hcl
resource "google_self_managed_vmware_engine_prepared_node" "prepared_node" {
  count              = var.node_count
  location           = var.zone
  prepared_node_id   = google_compute_instance.bare_metal_node[count.index].instance_id
  esxi_root_password = data.google_secret_manager_secret_version.esxi_root_pw.secret_data
  provider           = google-beta

  vcf_installer_metadata {
    appliance_fqdn          = var.vcf_installer_metadata.appliance_fqdn
    appliance_root_password = data.google_secret_manager_secret_version.vcf_appliance_root_pw.secret_data
    local_user_password     = data.google_secret_manager_secret_version.vcf_local_user_pw.secret_data
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

## Step 8: Outputs & Verification

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
- Verify that `terraform plan` successfully resolves conditional subnet, resource policy, and NEG lookups, and accesses Secret Manager.
- Verify private DNS configuration resolves your FQDN and hosts locally.
- After running `terraform apply`, confirm via the Google Cloud Console that the Bare Metal instances are registered in the regional Backend Service health checks.
