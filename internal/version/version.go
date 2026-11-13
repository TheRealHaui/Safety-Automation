package version

import "os"

// Commit is patched into the binary at link time by the Go linker via:
//
//	-ldflags="-X 'github.com/TheRealHaui/Safety-Automation/internal/version.Commit=<hash>[-dirty]'"
//
// The Makefile and .air.toml supply this automatically on every build; the Dockerfile
// receives it as a build ARG. When built with a plain "go run" outside the Makefile,
// the variable stays as the empty string (Go's zero value) and is omitted from Display.
//
// IMPORTANT — the git commit information always comes from the Docker image, never from
// the Helm chart. The Helm chart only carries Chart.appVersion (the semantic version
// string). The actual commit hash is baked into the binary inside the Docker image at
// image build time via:
//
//	docker build --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) ...
var Commit string

// Display is the full version string used in the web UI footer and the startup log.
// Assembled once in init() and immutable for the lifetime of the process.
// Examples: "0.1.1 (a3f2c1d)" when Helm-deployed, "dev (a3f2c1d-dirty)" locally.
var Display string

// Gos init() function runs
// only once true init
func init() {
	// APP_VERSION is injected by the Helm chart via ConfigMap (chart/templates/configmap.yaml).
	// It equals Chart.appVersion (e.g. "0.1.1") when deployed with Helm.
	// Falls back to "dev" for local runs so Display is never blank or misleading.
	Display = os.Getenv("APP_VERSION")

	if Display == "" {
		Display = " dev"
	}
	// Append the git commit hash so every running binary can be traced back to an
	// exact source state. "-dirty" suffix means uncommitted changes were present
	// at build time.
	if Commit != "" {

		// If the image was built without that flag, Commit stays at its Dockerfile default
		// "unknown" and Display will show e.g. "v0.1.1 (unknown)".
		if Commit == "unknown" {
			Commit = "unknown Git commit id"
		}

		Display += " (" + Commit + ")"

	}

}
