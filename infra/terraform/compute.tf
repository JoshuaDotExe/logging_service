data "oci_identity_availability_domains" "ads" {
  compartment_id = var.tenancy_ocid
}

data "oci_core_images" "oracle_linux" {
  compartment_id           = var.compartment_ocid
  operating_system         = "Oracle Linux"
  operating_system_version = var.oracle_linux_version
  shape                    = var.instance_shape
  state                    = "AVAILABLE"
  sort_by                  = "TIMECREATED"
  sort_order               = "DESC"
}

# Generating the SSH host key here lets Terraform output a known_hosts entry,
# so the deploy workflow can verify the server instead of trusting on first use.
# The private half lives in Terraform state and instance metadata; protect both.
resource "tls_private_key" "ssh_host" {
  algorithm = "ED25519"
}

locals {
  availability_domain = data.oci_identity_availability_domains.ads.availability_domains[var.availability_domain_index].name

  cloud_init = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    deploy_ssh_public_key = trimspace(var.deploy_ssh_public_key)
    host_private_key      = tls_private_key.ssh_host.private_key_openssh
    host_public_key       = trimspace(tls_private_key.ssh_host.public_key_openssh)
  })
}

resource "oci_core_instance" "app" {
  compartment_id      = var.compartment_ocid
  availability_domain = local.availability_domain
  display_name        = "${var.name_prefix}-app"
  shape               = var.instance_shape

  dynamic "shape_config" {
    for_each = var.instance_shape == "VM.Standard.A1.Flex" ? [1] : []
    content {
      ocpus         = var.instance_ocpus
      memory_in_gbs = var.instance_memory_gbs
    }
  }

  source_details {
    source_type             = "image"
    source_id               = data.oci_core_images.oracle_linux.images[0].id
    boot_volume_size_in_gbs = var.boot_volume_size_gbs
  }

  create_vnic_details {
    subnet_id        = oci_core_subnet.public.id
    assign_public_ip = false
    hostname_label   = "app"
    nsg_ids = [
      oci_core_network_security_group.web.id,
      oci_core_network_security_group.ssh.id,
    ]
  }

  instance_options {
    are_legacy_imds_endpoints_disabled = true
  }

  # user_data embeds the SSH host private key; keep it out of plan output.
  metadata = sensitive(merge(
    { user_data = base64encode(local.cloud_init) },
    var.admin_ssh_public_key == "" ? {} : { ssh_authorized_keys = trimspace(var.admin_ssh_public_key) },
  ))

  preserve_boot_volume = false

  lifecycle {
    # Newer images and cloud-init edits would otherwise force a replacement.
    # Rebuild deliberately with: terraform apply -replace=oci_core_instance.app
    ignore_changes = [source_details[0].source_id, metadata]
  }
}

data "oci_core_vnic_attachments" "app" {
  compartment_id = var.compartment_ocid
  instance_id    = oci_core_instance.app.id
}

data "oci_core_private_ips" "app" {
  vnic_id = data.oci_core_vnic_attachments.app.vnic_attachments[0].vnic_id
}

# A reserved IP survives instance replacement, so DNS does not need to change.
resource "oci_core_public_ip" "app" {
  compartment_id = var.compartment_ocid
  lifetime       = "RESERVED"
  display_name   = "${var.name_prefix}-ip"
  private_ip_id  = data.oci_core_private_ips.app.private_ips[0].id
}
