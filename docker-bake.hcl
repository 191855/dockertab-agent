variable "VERSION" {
  default = "dev"
}

variable "REGISTRY" {
  default = "ghcr.io/191855/dockertab-agent"
}

group "default" {
  targets = ["agent"]
}

target "agent" {
  dockerfile = "Dockerfile"
  platforms  = ["linux/amd64", "linux/arm64", "linux/arm/v7"]
  args = {
    VERSION = VERSION
  }
  tags = [
    "${REGISTRY}:${VERSION}",
    "${REGISTRY}:latest",
  ]
}
