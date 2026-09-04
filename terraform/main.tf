locals {
  chart = var.chart != "" ? var.chart : "${path.module}/../helm/brickie"

  set_values = concat(
    [
      {
        name  = "image.repository"
        value = var.image_repository
      },
      {
        name  = "image.tag"
        value = var.image_tag
      },
      {
        name  = "sandbox.image"
        value = var.sandbox_image
      },
      {
        name  = "sandbox.turnTimeout"
        value = var.turn_timeout
      },
      {
        name  = "agent-sandbox.enabled"
        value = tostring(var.agent_sandbox_enabled)
      },
      {
        name  = "csi.enabled"
        value = tostring(var.csi_enabled)
      },
      {
        name  = "replicaCount"
        value = tostring(var.replica_count)
      },
    ],
    var.sandbox_namespace != "" ? [{
      name  = "sandbox.namespace"
      value = var.sandbox_namespace
    }] : [],
    var.hatchet_existing_secret != "" ? [{
      name  = "hatchet.existingSecret"
      value = var.hatchet_existing_secret
    }] : [],
    var.existing_credentials_secret != "" ? [{
      name  = "sandbox.existingCredentialsSecret"
      value = var.existing_credentials_secret
    }] : [],
    var.csi_object_store != "" ? [{
      name  = "csi.objectStore"
      value = var.csi_object_store
    }] : [],
    [for i, name in var.credential_names : {
      name  = "sandbox.credentialNames[${i}]"
      value = name
    }],
  )

  set_sensitive_values = concat(
    var.hatchet_token != "" ? [{
      name  = "hatchet.token"
      value = var.hatchet_token
    }] : [],
    [for name, value in var.sandbox_credentials : {
      name  = "sandbox.credentials.${name}"
      value = value
    }],
  )
}

resource "helm_release" "brickie" {
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = var.create_namespace

  chart      = local.chart
  repository = var.chart_repository != "" ? var.chart_repository : null
  version    = var.chart_version

  timeout = var.helm_timeout
  atomic  = var.atomic
  wait    = true

  set           = local.set_values
  set_sensitive = local.set_sensitive_values
  values        = var.values

  lifecycle {
    precondition {
      condition     = (var.hatchet_token != "") != (var.hatchet_existing_secret != "")
      error_message = "Exactly one of hatchet_token or hatchet_existing_secret must be set."
    }
  }
}
