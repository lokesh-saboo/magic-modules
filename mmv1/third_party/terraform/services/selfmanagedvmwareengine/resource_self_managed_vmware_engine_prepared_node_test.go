package selfmanagedvmwareengine_test

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/hashicorp/terraform-provider-google/google/acctest"
	"github.com/hashicorp/terraform-provider-google/google/envvar"
	"github.com/hashicorp/terraform-provider-google/google/services/resourcemanager"
	"github.com/hashicorp/terraform-provider-google/google/tpgresource"
	transport_tpg "github.com/hashicorp/terraform-provider-google/google/transport"

	"google.golang.org/api/googleapi"
)

var (
	_ = fmt.Sprintf
	_ = log.Print
	_ = strconv.Atoi
	_ = strings.Trim
	_ = time.Now
	_ = resource.TestMain
	_ = terraform.NewState
	_ = envvar.TestEnvVar
	_ = tpgresource.SetLabels
	_ = transport_tpg.Config{}
	_ = googleapi.Error{}
)

func TestAccSelfManagedVmwareEnginePreparedNode_selfManagedVmwareEnginePreparedNodeCreate(t *testing.T) {
	t.Parallel()
	resourcemanager.BootstrapIamMembers(t, []resourcemanager.IamMember{
		{
			Member: "serviceAccount:service-{project_number}@gcp-sa-selfmanagedvmwareengine.iam.gserviceaccount.com",
			Role:   "roles/compute.viewer",
		},
	})

	randomSuffix := acctest.RandString(t, 10)

	esxiImage := os.Getenv("SELF_MANAGED_VMWARE_ENGINE_ESXI_IMAGE")
	if esxiImage == "" {
		esxiImage = "projects/<esxi-image-project>/global/images/<esxi-image>"
	}

	context := map[string]interface{}{
		"region":             envvar.GetTestRegionFromEnv(),
		"zone":               envvar.GetTestZoneFromEnv(),
		"random_suffix":      randomSuffix,
		"ip_range":           "10.0.0.0/24",
		"primary_ip":         "10.0.0.10",
		"esxi_image":         esxiImage,
		"esxi_root_password": "TerraformSMVETest@2026!",
	}

	acctest.VcrTest(t, resource.TestCase{
		PreCheck:                 func() { acctest.AccTestPreCheck(t) },
		ProtoV5ProviderFactories: acctest.ProtoV5ProviderBetaFactories(t),
		Steps: []resource.TestStep{
			{
				Config: testAccSelfManagedVmwareEnginePreparedNode_selfManagedVmwareEnginePreparedNodeCreate(context),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"google_self_managed_vmware_engine_prepared_node.example_prepared_node", "prepared_node_id",
						"google_compute_instance.test_instance", "instance_id",
					),
				),
			},
			{
				ResourceName:            "google_self_managed_vmware_engine_prepared_node.example_prepared_node",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"location", "prepared_node_id"},
			},
			{
				ResourceName:       "google_self_managed_vmware_engine_prepared_node.example_prepared_node",
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				ImportStateKind:    resource.ImportBlockWithResourceIdentity,
			},
		},
	})
}

func testAccSelfManagedVmwareEnginePreparedNode_selfManagedVmwareEnginePreparedNodeCreate(context map[string]interface{}) string {
	return acctest.Nprintf(`
# Create a network.
resource "google_compute_network" "test_network" {
  name                    = "test-network-%{random_suffix}"
  auto_create_subnetworks = false
}

# Create a subnetwork.
resource "google_compute_subnetwork" "test_subnetwork" {
  name          = "test-subnetwork-%{random_suffix}"
  ip_cidr_range = "%{ip_range}"
  region        = "%{region}"
  network       = google_compute_network.test_network.id
}

# Create a compute disk.
resource "google_compute_disk" "test_disk" {
  name  = "test-disk-%{random_suffix}"
  zone  = "%{zone}"
  type  = "hyperdisk-balanced"
  size  = 64
  image = "%{esxi_image}"

  guest_os_features {
    type = "IDPF"
  }
}

# Create a compute instance with ESXi image.
resource "google_compute_instance" "test_instance" {
  name         = "gce_instance-%{random_suffix}"
  zone     		 = "%{zone}"
  machine_type = "z3-highmem-192-metal"
  can_ip_forward = true

  boot_disk {
    source      = google_compute_disk.test_disk.id
    auto_delete = true
  }

  network_interface {
    subnetwork = google_compute_subnetwork.test_subnetwork.id
  }

  scheduling {
    on_host_maintenance = "TERMINATE"
    automatic_restart   = true
  }

  network_performance_config {
    total_egress_bandwidth_tier = "TIER_1"
  }
}

# Create a prepared node.
resource "google_self_managed_vmware_engine_prepared_node" "example_prepared_node" {
  location             = "%{zone}"
  prepared_node_id     = google_compute_instance.test_instance.instance_id
  esxi_root_password   = "%{esxi_root_password}"
  provider             = google-beta
}
`, context)
}
