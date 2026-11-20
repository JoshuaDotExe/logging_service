resource "oci_core_volume" "data" {
  compartment_id      = var.compartment_ocid
  availability_domain = local.availability_domain
  display_name        = "${var.name_prefix}-data"
  size_in_gbs         = var.data_volume_size_gbs

  lifecycle {
    # Holds the PostgreSQL data directory. Remove this guard deliberately
    # before running terraform destroy.
    prevent_destroy = true
  }
}

resource "oci_core_volume_attachment" "data" {
  attachment_type = "paravirtualized"
  instance_id     = oci_core_instance.app.id
  volume_id       = oci_core_volume.data.id
  device          = "/dev/oracleoci/oraclevdb"
  display_name    = "${var.name_prefix}-data"
}
