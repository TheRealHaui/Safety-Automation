package grafana

// DashboardUID is the fixed UID of the Safety Automation Resilience Dashboard, baked into
// chart/grafana/hoplon-resilience-dashboard.json at import time. No customer
// installation will ever use a different UID — this value is vendor-owned and
// does not need to be configurable.
// NOTE, whenever the Dashboard source gets replaced entirely by a different
// Dashboard that Dashboard UID has to get adapted here accordingly too.
const DashboardUID = "istio-workload-overview-version-two"

// OrgID is the numeric Grafana org ID the supplied token belongs to, auto-discovered
// via GET /api/org at startup and cached for the lifetime of the pod.
// Empty string means the startup probe did not succeed — the iframe org parameter
// will be absent and Grafana may route to the wrong org.
var OrgID string
