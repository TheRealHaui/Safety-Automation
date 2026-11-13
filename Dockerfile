# Dockerfile used by OpenShift to build and run the application.
# Multi-stage: Go builder produces the binary, runtime image copies only the binary in.
#
# Base image change from golang:bookworm to ubi9/go-toolset:
#   Red Hat Marketplace certification requires all stages to be based on UBI.
#   The ubi9/go-toolset image is Red Hat's official Go builder on UBI9 and is
#   pre-certified, so it passes the marketplace scanner without extra work.

# ---- build stage ----
# 1.25 is the highest Go version currently shipped by ubi9/go-toolset (Go 1.25.8
# as of the January 2026 image release). Update this tag when Red Hat ships 1.26.

# On version upgrade of the UBI image here always search for microdnf update
# in the Dockerfile here and do as the comment there requests
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder


# ubi9/go-toolset sets USER 1001 by default. Switch to root for the build stage
# so package installation and WORKDIR setup have no permission surprises.
# This is fine — the builder is a throw-away image; the runtime USER 1001 below
# is where the security constraint actually matters.
USER root

WORKDIR /build

# Go shall use cache for build on OpenShift.
ENV GOCACHE=/tmp/go-build


# Old comment review later
# #Necessary to later install
  ##the native Oracle client
  ##Further, note we have to build
  ##this always this way because
  ##Oracle does not allow its client
  ##to be distributed by public
  ##Docker images whatever way.
# Old comment

# On RHEL/UBI the libaio package is named 'libaio', not 'libaio1' (Debian naming).
# gcc is required because CGO_ENABLED=1 is mandatory for the Oracle godror driver,
# which wraps the Oracle OCI C library via cgo.
# --nodocs keeps the image layer small; dnf clean all removes the package cache.
RUN dnf install -y --nodocs gcc libaio && \
    dnf clean all

COPY go.mod go.sum ./

# Download modules using BuildKit persistent cache.
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the rest of the source code.
COPY . .

#Old comment
#Build using Go build cache to speed up incremental builds
#Especially important because of Oracle client we have to
#use CGO_ENABLED=1. This in conjunction with the previous
#go mod download enables cached build dependencies for Go
#Old comment

# GIT_COMMIT is resolved on the host before the build and passed in as a build argument:
#   docker build --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) ...
# This avoids installing git inside the builder image just to read the commit hash.
# Dirty-state detection ("-dirty" suffix) is intentionally omitted: a Docker image build
# should always come from a clean, committed state. If the source is dirty the CI pipeline
# should fail before reaching this step, not silently embed a "-dirty" label in the image.
# Defaults to "unknown" so the footer still renders if the build arg is not supplied.
ARG GIT_COMMIT=unknown

# CGO_ENABLED=1: mandatory for godror (Oracle OCI C library via cgo).
# -buildvcs=false: suppresses VCS stamp errors that occur inside OCI build environments
#   where the .git directory may not be readable or well-formed.
# -ldflags="-s -w": strips debug info and DWARF tables to shrink the binary.
# -X patches gitCommit into the binary so the web UI footer shows the exact image commit
#   (e.g. "0.1.1 (a3f2c1d)") — matching what "make run" shows during local development.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/go-build \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -buildvcs=false \
    -ldflags="-s -w -X 'github.com/TheRealHaui/Safety-Automation/internal/version.Commit=${GIT_COMMIT}'" \
    -o hoplon ./cmd/server


# ---------- runtime stage ----------
# ubi9-minimal is the smallest UBI9 variant that still includes microdnf,
# so we can install runtime dependencies (libaio, unzip, ca-certificates).
# Red Hat does not publish a plain ':9' floating tag — use the current minor version.
# Update to '9.8' etc. when Red Hat ships the next UBI9 minor release.
FROM registry.access.redhat.com/ubi9/ubi-minimal:9.7

# Required labels for Red Hat Marketplace container certification.
# The certification scanner reads labels from the FINAL image only — labels
# in the builder stage are discarded and invisible to the scanner.
# Update 'vendor' and 'version' to match your organisation and release cycle.
LABEL name="hoplon" \
      vendor="DynaCov Inc." \
      maintainer="DynaCov Inc. <michael.hausegger@hausegger.tech>" \
      version="1.0" \
      release="1" \
      summary="Resilience Testing Platform for OpenShift" \
      description="Executes chaos/resilience scenario tests against Deployments and DeploymentConfigs on OpenShift clusters, with live log streaming and PDF audit report generation."

#We need the Oracle native client installed
#because the Godor Oracle library we use
#needs it. Further, there is no official
#Go driver/client available by Oracle for Go
# libaio: runtime dependency of the Oracle Instant Client ('libaio', not 'libaio1' — that is Debian naming).
# unzip: needed to extract the embedded Instant Client zip below.
# ca-certificates: needed for TLS to Oracle Cloud and the OpenShift API.
# microdnf is the package manager in ubi9-minimal (stripped-down dnf, smaller footprint).
# libcap CVE-2026-4878 (HIGH): privilege escalation via TOCTOU in cap_set_file().
# Fix available in 2.48-10.el9_7.1 — upgrade explicitly so the certified image is clean.
RUN microdnf install -y --nodocs libaio unzip ca-certificates && \
    microdnf clean all


#This secion is only and solely to solve CVEs
#that were found by Trivy and Grype
#After every installation/upgrade of the Red
#Hat UBI this section has to get cleaned

# libcap CVE-2026-4878 (HIGH): privilege escalation via TOCTOU in cap_set_file().
# Fix available in 2.48-10.el9_7.1 — upgrade explicitly so the certified image is clean.
RUN microdnf update -y libcap glib2 krb5-libs p11-kit  openssl-libs systemd-libs \
    glibc glibc-common glibc-minimal-langpack


# Clean important
RUN microdnf clean all



WORKDIR /opt/oracle

# The Oracle Instant Client cannot be distributed via public Docker Hub images per
# Oracle's license, so it is embedded in resources-embedded/ and copied from there.
COPY resources-embedded/instantclient-basiclite-linux.x64-23.26.1.0.0.zip .

RUN unzip instantclient-basiclite-linux.x64-23.26.1.0.0.zip && \
    rm instantclient-basiclite-linux.x64-23.26.1.0.0.zip


# TNS_ADMIN must be set at image build time, not at Go application start time.
# Further, at least at development time we have to use the Oracle wallet for
# the Oracle Cloud database used for development. The Oracle wallet is a
# digital certificate. See directory resources-local-development-only in the
# source code for more details. This directory is not part of the Docker build.
# The Oracle native C library reads this env var when the shared library loads
# (before main() runs) — setting it later in Go code is too late and causes
# misleading ORA-12506 ACL filtering errors.
# The Helm chart mounts the wallet Secret at exactly this path so the Oracle
# client finds it regardless of whether the wallet is baked in or volume-mounted.
ENV TNS_ADMIN=/opt/oracle/instantclient_23_26/network/admin

RUN chmod -R g=u /opt/oracle

ENV LD_LIBRARY_PATH=/opt/oracle/instantclient_23_26

WORKDIR /app

# Red Hat preflight tool has license check
# The image must contain a file under /licenses/.
COPY LICENSE /licenses/LICENSE
# LiberationMono (SIL OFL 1.1) is compiled into the binary via go:embed for the
# PDF/A-1b reports. OFL §2 requires the license to accompany every redistributed
# copy of the font, and the image is such a copy.
COPY resources-embedded/needed-for-pdfa1b-workaround/LiberationMono-LICENSE.txt /licenses/LiberationMono-LICENSE.txt

COPY --from=builder /build/hoplon .


EXPOSE 8080


#First create a non-root user
#with a specific UID (1001 in this case)
#OpenShift expects the user to
#be not root or banks or whatever
#IT-Security related totally
#fine and actually expected

# UID 1001 satisfies OpenShift's default restricted SCC (no root allowed).
# UBI9 images are built with this expectation, so no adduser step is needed.
USER 1001

CMD ["./hoplon"]
