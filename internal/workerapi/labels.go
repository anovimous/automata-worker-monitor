package workerapi

// Labels of a worker container.
const (
	LabelID            = "automata.worker.id"
	LabelManagedBy     = "automata.worker.managed-by"
	LabelTemplate      = "automata.worker.template"
	LabelNetworkMode   = "automata.worker.network-mode"
	LabelTelemetryPort = "automata.worker.telemetry-port"
	LabelCreatedAt     = "automata.worker.created-at"

	// ManagedByMonitor is the value of LabelManagedBy on containers created by the monitor.
	ManagedByMonitor = "automata-worker-monitor"
)
