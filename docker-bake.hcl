variable "DEFAULT_TAG" {
  default = "dexd-mikrotik:local"
}

// Baked into the binary via -ldflags. The release workflow sets both from
// the tag and commit; local builds report "dev".
variable "VERSION" {
  default = "dev"
}

variable "REVISION" {
  default = "dev"
}

// Special target: https://github.com/docker/metadata-action#bake-definition
target "docker-metadata-action" {
  tags = ["${DEFAULT_TAG}"]
}

group "default" {
  targets = ["image-local"]
}

target "image" {
  inherits = ["docker-metadata-action"]
  args = {
    VERSION  = VERSION
    REVISION = REVISION
  }
  // GHCR links a package to its repo from the manifest ANNOTATION, not the
  // config label -- the label alone leaves the package orphaned.
  annotations = [
    "index,manifest:org.opencontainers.image.source=https://github.com/greyrock-labs/dexd-mikrotik"
  ]
}

target "image-local" {
  inherits = ["image"]
  output = ["type=docker"]
}

target "image-all" {
  inherits = ["image"]
  // The Forgejo runner cannot mount binfmt_misc, so QEMU is unavailable --
  // do not add setup-qemu-action. arm64 still works because the Dockerfile
  // cross-compiles on the build platform and the runtime stage has no RUN.
  platforms = [
    "linux/amd64",
    "linux/arm64"
  ]
}
