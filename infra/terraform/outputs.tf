output "public_ip" {
  description = "Reserved public IP. Point your DNS A record here and store it as the DEPLOY_HOST GitHub variable."
  value       = oci_core_public_ip.app.ip_address
}

output "ssh_known_hosts" {
  description = "Store as the DEPLOY_SSH_KNOWN_HOSTS GitHub variable."
  value       = "${oci_core_public_ip.app.ip_address} ${trimspace(tls_private_key.ssh_host.public_key_openssh)}"
}

output "ssh_nsg_id" {
  description = "Store as the OCI_SSH_NSG_ID GitHub variable. Deploys open SSH here transiently."
  value       = oci_core_network_security_group.ssh.id
}

output "instance_id" {
  value = oci_core_instance.app.id
}

output "availability_domain" {
  value = local.availability_domain
}
