package generationbudget

// AutoWorkload is independent of the shared GPU request. A manual shared
// ceiling bounds an Auto workload without disabling its measurements.
func (b *Budget) AutoWorkload(w Workload) bool {
	if b == nil {
		return false
	}
	if !b.independentAuto || w.Kind == OtherWorkload {
		return b.AutoGPU()
	}
	return true
}
func (b *Budget) CanTuneWorkload(w Workload) bool {
	return b.AutoWorkload(w) && b.requested.MaxProcesses != 1 && b.requested.MaxGPUProcesses != 1
}

// Independent Auto protects only its workload families. Positive manual limits
// for other rendering types retain their existing admission and accounting.
func (b *Budget) resourceProtected(w Workload) bool { return b.AutoWorkload(w) }

// Caller holds mu. Family ceilings count lanes across files and scoped plans.
func (b *Budget) workloadLimit(kind WorkloadKind) int {
	if !b.independentAuto || kind == OtherWorkload {
		return b.sharedGPULimit
	}
	if kind == SpriteWorkload {
		return b.spriteLimit
	}
	return b.previewLimit
}
func (b *Budget) updateWorkloadCapacity(w Workload, current int) {
	if !b.independentAuto {
		b.updateCapacity(current)
		return
	}
	sprite, preview := 1, 1
	for _, c := range b.learning {
		if c.kind == SpriteWorkload {
			sprite = max(sprite, c.limit)
		}
		if c.kind == PreviewWorkload {
			preview = max(preview, c.limit)
		}
	}
	if w.Kind == SpriteWorkload {
		sprite = max(sprite, current)
	}
	if w.Kind == PreviewWorkload {
		preview = max(preview, current)
	}
	shared := b.requested.MaxGPUProcesses
	if shared == 0 {
		shared = max(sprite, preview, current, 1)
		for _, c := range b.learning {
			shared = max(shared, c.limit)
		}
	}
	if b.requested.MaxProcesses > 0 {
		shared = min(shared, b.requested.MaxProcesses)
	}
	b.sharedGPULimit, b.settings.MaxGPUProcesses = shared, shared
	b.spriteLimit, b.previewLimit = min(sprite, shared), min(preview, shared)
	if b.requested.MaxProcesses == 0 {
		b.settings.MaxProcesses = max(b.cpuLimit, shared)
		if b.requested.Threads == 0 {
			b.settings.Threads = max(1, b.resources().CPUs/b.settings.MaxProcesses)
		}
	}
}

// NewIndependentAutoForDevice keeps independent sprite and preview tuning under
// the existing shared requests. It adds no saved configuration or override.
func NewIndependentAutoForDevice(s Settings, device string) (*Budget, error) {
	return NewIndependentAutoWithResources(s, func() Resources { return DetectResources(device) })
}
func NewIndependentAutoWithResources(s Settings, resources func() Resources, processProbe ...func(int) ProcessResources) (*Budget, error) {
	b, err := NewWithResources(s, resources, processProbe...)
	if err != nil {
		return nil, err
	}
	b.independentAuto = true
	b.updateWorkloadCapacity(Workload{}, 1)
	return b, nil
}
