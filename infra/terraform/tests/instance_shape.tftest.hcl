mock_provider "oci" {
  mock_data "oci_identity_availability_domains" {
    defaults = {
      availability_domains = [{ name = "test-ad" }]
    }
  }

  mock_data "oci_core_images" {
    defaults = {
      images = [{ id = "test-image" }]
    }
  }
}

mock_provider "tls" {}

variables {
  tenancy_ocid          = "test-tenancy"
  user_ocid             = "test-user"
  fingerprint           = "test-fingerprint"
  private_key           = "test-key"
  region                = "test-region"
  compartment_ocid      = "test-compartment"
  deploy_ssh_public_key = "ssh-ed25519 test-key"
}

run "a1_default" {
  command = plan

  assert {
    condition     = oci_core_instance.app.shape == "VM.Standard.A1.Flex"
    error_message = "The default shape must remain A1 Flex."
  }

  assert {
    condition     = data.oci_core_images.oracle_linux.shape == "VM.Standard.A1.Flex"
    error_message = "The Oracle Linux image search must match the A1 shape."
  }

  assert {
    condition     = oci_core_instance.app.shape_config[0].ocpus == 2 && oci_core_instance.app.shape_config[0].memory_in_gbs == 12
    error_message = "A1 Flex must retain its CPU and memory defaults."
  }
}

run "amd_micro_rejected" {
  command = plan

  variables {
    instance_shape = "VM.Standard.E2.1.Micro"
  }

  expect_failures = [var.instance_shape]
}

run "invalid_shape" {
  command = plan

  variables {
    instance_shape = "invalid"
  }

  expect_failures = [var.instance_shape]
}
