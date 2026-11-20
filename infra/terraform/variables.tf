variable "tenancy_ocid" {
  description = "OCID of the OCI tenancy."
  type        = string
}

variable "user_ocid" {
  description = "OCID of the OCI user that owns the API signing key."
  type        = string
}

variable "fingerprint" {
  description = "Fingerprint of the OCI API signing key."
  type        = string
}

variable "private_key" {
  description = "PEM contents of the OCI API signing private key."
  type        = string
  sensitive   = true
}

variable "region" {
  description = "OCI region identifier, for example uk-london-1. Always Free compute must be created in the tenancy home region."
  type        = string
}

variable "compartment_ocid" {
  description = "OCID of the compartment that will hold every resource."
  type        = string
}

variable "name_prefix" {
  description = "Prefix used for display names."
  type        = string
  default     = "logging-service"
}

variable "vcn_cidr" {
  type    = string
  default = "10.20.0.0/16"
}

variable "subnet_cidr" {
  type    = string
  default = "10.20.1.0/24"
}

variable "availability_domain_index" {
  description = "Index into the region's availability domains. Change this if A1 capacity is unavailable in the current AD."
  type        = number
  default     = 0
}

variable "instance_ocpus" {
  description = "A1 OCPUs. Keep within your tenancy's Always Free A1 allowance."
  type        = number
  default     = 2
}

variable "instance_memory_gbs" {
  description = "A1 memory in GB. Keep within your tenancy's Always Free A1 allowance."
  type        = number
  default     = 12
}

variable "ubuntu_version" {
  description = "Canonical Ubuntu release used for the instance image."
  type        = string
  default     = "24.04"
}

variable "boot_volume_size_gbs" {
  description = "Boot volume size. Boot plus data volumes must stay within the 200 GB Always Free block storage allowance."
  type        = number
  default     = 50
}

variable "data_volume_size_gbs" {
  description = "Size of the persistent block volume that stores PostgreSQL data."
  type        = number
  default     = 50
}

variable "deploy_ssh_public_key" {
  description = "OpenSSH public key for the `deploy` user used by GitHub Actions."
  type        = string

  validation {
    condition     = can(regex("^ssh-(ed25519|rsa) ", var.deploy_ssh_public_key))
    error_message = "The deploy_ssh_public_key value must be an OpenSSH ed25519 or RSA public key."
  }
}

variable "admin_ssh_public_key" {
  description = "Optional OpenSSH public key for the default `ubuntu` admin user. Empty disables admin key injection."
  type        = string
  default     = ""
}

variable "admin_ssh_cidrs" {
  description = "CIDRs permanently allowed to reach SSH. Leave empty; the deploy workflow opens SSH to its own runner IP only for the duration of a deploy."
  type        = list(string)
  default     = []
}
