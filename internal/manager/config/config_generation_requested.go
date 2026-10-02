package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/stashapp/stash/pkg/generationbudget"
)

// GenerationConfiguration describes persisted requests, not device capability or
// the actual backend used by an individual job. Zero limits mean conservative auto.
type GenerationConfiguration struct {
	MarkerBackend   string
	SpriteBackend   string
	Device          string
	BudgetEnabled   bool
	MaxProcesses    int
	MaxGPUProcesses int
	Threads         int
}

type GenerationConfigurationPatch struct {
	MarkerBackend   *string
	SpriteBackend   *string
	Device          *string
	BudgetEnabled   *bool
	MaxProcesses    *int
	MaxGPUProcesses *int
	Threads         *int
}

func (p GenerationConfigurationPatch) values() map[string]interface{} {
	values := map[string]interface{}{}
	if p.MarkerBackend != nil {
		values[MarkerGenerationBackend] = *p.MarkerBackend
	}
	if p.SpriteBackend != nil {
		values[SpriteGenerationBackend] = *p.SpriteBackend
	}
	if p.Device != nil {
		values[GenerationDevice] = *p.Device
	}
	if p.BudgetEnabled != nil {
		values[GenerationBudgetEnabled] = *p.BudgetEnabled
	}
	if p.MaxProcesses != nil {
		values[GenerationMaxProcesses] = *p.MaxProcesses
	}
	if p.MaxGPUProcesses != nil {
		values[GenerationMaxGPUProcesses] = *p.MaxGPUProcesses
	}
	if p.Threads != nil {
		values[GenerationThreads] = *p.Threads
	}
	return values
}

// readRequestedGeneration assumes the config lock is held. Validate a merged
// proposal without touching either the saved request or running scheduler.
func (i *Config) readRequestedGeneration(proposal map[string]interface{}) (GenerationConfiguration, error) {
	s := GenerationConfiguration{MarkerBackend: "software", SpriteBackend: "software", Device: "/dev/dri/renderD128"}
	get := func(key string) (interface{}, bool) {
		if v, ok := proposal[key]; ok {
			return v, true
		}
		v := i.forKey(key)
		return v.Get(key), v.Exists(key)
	}
	for _, field := range []struct {
		key  string
		dest *string
	}{
		{MarkerGenerationBackend, &s.MarkerBackend}, {SpriteGenerationBackend, &s.SpriteBackend}, {GenerationDevice, &s.Device},
	} {
		if v, ok := get(field.key); ok {
			value := fmt.Sprint(v)
			// Older YAML may contain an empty optional backend/device; preserve
			// its default. Explicit API requests must select valid values.
			_, proposed := proposal[field.key]
			if value != "" || proposed {
				*field.dest = value
			}
		}
	}
	for _, backend := range []string{s.MarkerBackend, s.SpriteBackend} {
		switch backend {
		case "software", "qsv", "vaapi":
		default:
			return s, fmt.Errorf("generation backend must be software, qsv or vaapi")
		}
	}
	if !regexp.MustCompile(`^/dev/dri/renderD[0-9]+$`).MatchString(s.Device) {
		return s, fmt.Errorf("%s must select an absolute /dev/dri/renderD device", GenerationDevice)
	}
	if v, ok := get(GenerationBudgetEnabled); ok {
		switch value := v.(type) {
		case bool:
			s.BudgetEnabled = value
		case string:
			if value != "true" && value != "false" {
				return s, fmt.Errorf("%s must be boolean", GenerationBudgetEnabled)
			}
			s.BudgetEnabled = value == "true"
		default:
			return s, fmt.Errorf("%s must be boolean", GenerationBudgetEnabled)
		}
	}
	for _, field := range []struct {
		key  string
		dest *int
	}{
		{GenerationMaxProcesses, &s.MaxProcesses}, {GenerationMaxGPUProcesses, &s.MaxGPUProcesses}, {GenerationThreads, &s.Threads},
	} {
		if v, ok := get(field.key); ok {
			switch v.(type) {
			case float32, float64, bool:
				return s, fmt.Errorf("%s must be an integer or auto", field.key)
			}
			raw := fmt.Sprint(v)
			if raw == "auto" {
				continue
			}
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return s, fmt.Errorf("%s must be an integer or auto", field.key)
			}
			*field.dest = parsed
		}
	}
	_, err := s.Limits().Normalize()
	return s, err
}

func (s GenerationConfiguration) Limits() generationbudget.Settings {
	return generationbudget.Settings{MaxProcesses: s.MaxProcesses, MaxGPUProcesses: s.MaxGPUProcesses, Threads: s.Threads}
}

func (i *Config) GetRequestedGenerationConfiguration() (GenerationConfiguration, error) {
	i.RLock()
	defer i.RUnlock()
	return i.readRequestedGeneration(nil)
}

func (i *Config) GetActiveGenerationConfiguration() GenerationConfiguration {
	s := i.generation()
	active := s.requested
	active.MarkerBackend, active.SpriteBackend, active.Device = s.marker, s.sprite, s.device
	active.BudgetEnabled = s.budget != nil
	if s.budget != nil {
		limits := s.budget.Settings()
		active.MaxProcesses, active.MaxGPUProcesses, active.Threads = limits.MaxProcesses, limits.MaxGPUProcesses, limits.Threads
	}
	return active
}

func (i *Config) GenerationRestartRequired() bool {
	active := i.generation()
	requested, err := i.GetRequestedGenerationConfiguration()
	return err != nil || requested != active.requested
}

func (i *Config) validateGenerationPatch(p GenerationConfigurationPatch) error {
	values := p.values()
	if len(values) == 0 {
		return nil
	}
	for key := range values {
		if i.overrides.Exists(key) {
			return fmt.Errorf("cannot set overridden value: %s", key)
		}
	}
	_, err := i.readRequestedGeneration(values)
	return err
}

func (i *Config) ValidateGenerationConfigurationPatch(p GenerationConfigurationPatch) error {
	i.RLock()
	defer i.RUnlock()
	return i.validateGenerationPatch(p)
}

func (i *Config) ApplyGenerationConfigurationPatch(p GenerationConfigurationPatch) error {
	if len(p.values()) == 0 {
		return nil
	}
	// Capture active values before a save, even if no generation job has run yet.
	i.generation()
	i.Lock()
	defer i.Unlock()
	if err := i.validateGenerationPatch(p); err != nil {
		return err
	}
	for key, value := range p.values() {
		i.set(key, value)
	}
	return nil
}

// WriteGenerationConfigurationPatch persists a validated generation proposal.
// Restore generation requests after a failed write so a query cannot report
// an unpersisted generation proposal as saved. Other general settings retain
// their existing ConfigureGeneral write behavior.
func (i *Config) WriteGenerationConfigurationPatch(p GenerationConfigurationPatch) error {
	return i.writeGenerationConfigurationPatch(p, func(path string, data []byte) error {
		return writeGenerationConfigAtomically(path, data, generationConfigFileOps{})
	})
}

// Dependencies are supplied per call for fault tests, never changed globally.
func (i *Config) writeGenerationConfigurationPatch(p GenerationConfigurationPatch, persist func(string, []byte) error) error {
	values := p.values()
	if len(values) == 0 {
		return i.Write()
	}
	i.generation()
	i.Lock()
	defer i.Unlock()
	if err := i.validateGenerationPatch(p); err != nil {
		return err
	}
	old := make(map[string]interface{}, len(values))
	for key, value := range values {
		if i.main.Exists(key) {
			old[key] = i.main.Get(key)
		}
		i.set(key, value)
	}
	data, err := i.marshal()
	if err == nil {
		err = persist(i.filePath, data)
	}
	if err != nil {
		for key := range values {
			i.set(key, old[key])
		}
	}
	return err
}

// generationConfigFileOps permits deterministic short-write/publish failures in
// tests while keeping all production saves on the same atomic publication path.
type generationConfigFileOps struct {
	write   func(*os.File, []byte) (int, error)
	publish func(string, string) error
}

func writeGenerationConfigAtomically(destination string, data []byte, ops generationConfigFileOps) error {
	mode := os.FileMode(0640)
	if info, err := os.Stat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("configuration destination must be a regular file")
		}
		mode = info.Mode().Perm()
		// Keep an existing symlink intact: publish beside its resolved target.
		resolved, err := filepath.EvalSymlinks(destination)
		if err != nil {
			return err
		}
		destination = resolved
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".generation-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	write := ops.write
	if write == nil {
		write = func(file *os.File, bytes []byte) (int, error) { return file.Write(bytes) }
	}
	written, err := write(temporary, data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	publish := ops.publish
	if publish == nil {
		publish = os.Rename
	}
	return publish(temporary.Name(), destination)
}
