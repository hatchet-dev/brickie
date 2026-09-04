variable "release_name" {
  description = "Helm release name."
  type        = string
  default     = "brickie"
}

variable "namespace" {
  description = "Namespace to install the brickie controller into."
  type        = string
  default     = "brickie"
}

variable "create_namespace" {
  description = "Create the namespace if it does not exist."
  type        = bool
  default     = true
}

variable "chart" {
  description = "Chart reference. Defaults to the chart bundled with this module. Set together with chart_repository to install from a chart repository instead."
  type        = string
  default     = ""
}

variable "chart_repository" {
  description = "Chart repository URL. Leave empty when installing from a local path."
  type        = string
  default     = ""
}

variable "chart_version" {
  description = "Chart version constraint. Only used with chart_repository."
  type        = string
  default     = null
}

variable "hatchet_token" {
  description = "Hatchet client token for the tenant. Mutually exclusive with hatchet_existing_secret."
  type        = string
  default     = ""
  sensitive   = true
}

variable "hatchet_existing_secret" {
  description = "Name of an existing Kubernetes Secret containing a HATCHET_CLIENT_TOKEN key. Mutually exclusive with hatchet_token."
  type        = string
  default     = ""
}

variable "image_repository" {
  description = "Controller image repository."
  type        = string
  default     = "brickie-worker"
}

variable "image_tag" {
  description = "Controller image tag."
  type        = string
  default     = "latest"
}

variable "sandbox_image" {
  description = "Default image for sandbox pods."
  type        = string
  default     = "alpine:3"
}

variable "sandbox_namespace" {
  description = "Namespace sandbox pods run in. Defaults to the release namespace."
  type        = string
  default     = ""
}

variable "turn_timeout" {
  description = "Execution timeout per sandbox turn, as a Go duration."
  type        = string
  default     = "15m"

  validation {
    condition     = can(regex("^[0-9]+(ns|us|ms|s|m|h)$", var.turn_timeout))
    error_message = "turn_timeout must be a Go duration such as 15m or 1h."
  }
}

variable "sandbox_credentials" {
  description = "Environment variables to inject into sandbox pods, as a map of name to value. Stored in a chart-managed Secret."
  type        = map(string)
  default     = {}
  sensitive   = true
}

variable "existing_credentials_secret" {
  description = "Name of an existing Kubernetes Secret whose keys are injected into sandbox pods. Use credential_names to select the keys."
  type        = string
  default     = ""
}

variable "credential_names" {
  description = "Environment variable names to inject into sandbox pods when using existing_credentials_secret."
  type        = list(string)
  default     = []
}

variable "agent_sandbox_enabled" {
  description = "Install the agent-sandbox controller and CRDs as a subchart. Disable when the cluster already runs agent-sandbox."
  type        = bool
  default     = true
}

variable "csi_enabled" {
  description = "Deploy the brickie CSI node DaemonSet for overlayfs workspaces."
  type        = bool
  default     = false
}

variable "csi_object_store" {
  description = "Object store URL for snapshot layers: s3://, gs://, azblob://, or s3:// with ?endpoint= for S3-compatible stores. Empty disables offload."
  type        = string
  default     = ""
}

variable "replica_count" {
  description = "Controller replicas. Keep at 1 until snapshot storage is fully object-store backed."
  type        = number
  default     = 1
}

variable "values" {
  description = "Additional Helm values as YAML strings, merged after the module-managed values."
  type        = list(string)
  default     = []
}

variable "helm_timeout" {
  description = "Timeout in seconds for Helm operations."
  type        = number
  default     = 300
}

variable "atomic" {
  description = "Roll back the release on failed install or upgrade."
  type        = bool
  default     = true
}
