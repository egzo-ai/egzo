# The images, as one build graph: `docker buildx bake` builds the egzo image, the harness base on it and the harness images on
# that, without pushing anything in between (each one takes the one before it as a named context). CI and the release build it once
# per architecture, each on a runner of that architecture; the Makefile's `docker build` targets do the same without buildx.
#   docker buildx bake                     # every image, for this machine's architecture, loaded into the engine
#   docker buildx bake hub                 # one of them

variable "VERSION" { default = "dev" }
variable "REGISTRY" { default = "ghcr.io/egzo-ai" }
variable "HARNESSES" { default = ["claude-code", "opencode"] }
# The architecture, for the cache: one cache per architecture, so the two runners do not overwrite each other's. Empty: no
# registry cache (a local build).
variable "CACHE_ARCH" { default = "" }
# true when the cache can be written (main and releases); a pull request only reads it.
variable "CACHE_WRITE" { default = false }
# true for the release, which pushes by digest: an image pushed by digest is named by its repository alone, without a tag (the
# tags are put on afterwards, on the multi-architecture manifest).
variable "PUSH_BY_DIGEST" { default = false }

group "default" {
  targets = ["egzo", "hub", "harness-base", "harness"]
}

function "tags" {
  params = [image]
  result = PUSH_BY_DIGEST ? ["${REGISTRY}/${image}"] : ["${REGISTRY}/${image}:${VERSION}"]
}

function "cache_from" {
  params = [image]
  result = CACHE_ARCH == "" ? [] : ["type=registry,ref=${REGISTRY}/${image}:buildcache-${CACHE_ARCH}"]
}

function "cache_to" {
  params = [image]
  result = CACHE_ARCH == "" || !CACHE_WRITE ? [] : ["type=registry,ref=${REGISTRY}/${image}:buildcache-${CACHE_ARCH},mode=max"]
}

target "_common" {
  labels = {
    "org.opencontainers.image.source"   = "https://github.com/egzo-ai/egzo"
    "org.opencontainers.image.version"  = VERSION
    "org.opencontainers.image.licenses" = "AGPL-3.0-only"
  }
}

target "egzo" {
  inherits   = ["_common"]
  context    = "."
  args       = { VERSION = VERSION }
  tags       = tags("egzo")
  cache-from = cache_from("egzo")
  cache-to   = cache_to("egzo")
}

# The hub imports core as source, so it does not need the egzo image.
target "hub" {
  inherits   = ["_common"]
  context    = "."
  dockerfile = "hub/Dockerfile"
  args       = { VERSION = VERSION }
  tags       = tags("egzo-hub")
  cache-from = cache_from("egzo-hub")
  cache-to   = cache_to("egzo-hub")
}

target "harness-base" {
  inherits   = ["_common"]
  context    = "harness/base"
  contexts   = { egzo-image = "target:egzo" }
  args       = { EGZO_IMAGE = "egzo-image" }
  tags       = tags("egzo-harness-base")
  cache-from = cache_from("egzo-harness-base")
  cache-to   = cache_to("egzo-harness-base")
}

target "harness" {
  name       = "harness-${h}"
  matrix     = { h = HARNESSES }
  inherits   = ["_common"]
  context    = "harness/${h}"
  contexts   = { base-image = "target:harness-base" }
  args       = { BASE_IMAGE = "base-image" }
  tags       = tags("egzo-harness-${h}")
  cache-from = cache_from("egzo-harness-${h}")
  cache-to   = cache_to("egzo-harness-${h}")
}
