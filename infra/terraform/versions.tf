terraform {
  required_version = ">= 1.12.0"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "~> 9.13"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.4"
    }
  }

  # Partial configuration. bucket, namespace and credentials are supplied by
  # `terraform init -backend-config=...` (see .github/workflows/terraform.yml).
  backend "oci" {
    key = "logging-service/terraform.tfstate"
  }
}

provider "oci" {
  tenancy_ocid = var.tenancy_ocid
  user_ocid    = var.user_ocid
  fingerprint  = var.fingerprint
  private_key  = var.private_key
  region       = var.region
}
