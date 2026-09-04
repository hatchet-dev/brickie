output "release_name" {
  description = "Name of the Helm release."
  value       = helm_release.brickie.name
}

output "namespace" {
  description = "Namespace the controller is installed into."
  value       = helm_release.brickie.namespace
}

output "sandbox_namespace" {
  description = "Namespace sandbox pods run in."
  value       = var.sandbox_namespace != "" ? var.sandbox_namespace : helm_release.brickie.namespace
}

output "chart_version" {
  description = "Deployed chart version."
  value       = helm_release.brickie.version
}
