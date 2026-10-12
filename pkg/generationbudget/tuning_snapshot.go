package generationbudget

// MeasurementState qualifies a workload-type Auto selection. It says nothing
// about universal hardware capability, or the grant an individual job receives.
type MeasurementState string

const (
	NotMeasured MeasurementState = "NOT_MEASURED"
	Adapting    MeasurementState = "ADAPTING"
	Measured    MeasurementState = "MEASURED"
)

type TuningSnapshot struct {
	AutomaticConcurrency *int
	MeasurementState     MeasurementState
}

// WorkloadTuning returns a read-only, detached snapshot. It performs no resource
// probes, admissions, calibration or learning. Internal per-plan keys stay private.
// The visible selection is the active aggregate ceiling, but only a validated
// complete-output count can qualify it as measured. Untested trial counts and
// resource seeds remain unavailable as measured values.
func (b *Budget) WorkloadTuning(kind WorkloadKind) TuningSnapshot {
	result := TuningSnapshot{MeasurementState: NotMeasured}
	if b == nil || !b.independentAuto || kind != SpriteWorkload && kind != PreviewWorkload {
		return result
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	selected := b.workloadLimit(kind)
	for _, c := range b.learning {
		if c.kind != kind || c.canonicalLimit < 1 {
			continue
		}
		result.MeasurementState = Adapting
		if c.limit >= selected && c.canonicalLimit == selected {
			value := selected
			result.AutomaticConcurrency = &value
			result.MeasurementState = Measured
			return result
		}
	}
	return result
}
