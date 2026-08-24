package validation

// ValidationSink consumes one borrowed validation finding and decides
// whether the sweep continues (Rust ValidationSink +
// ValidationSinkControl). A nil sink (or a nil function adapter)
// behaves like Continue for every finding.
type ValidationSink interface {
	Finding(*ValidationFinding) (ValidationSinkControl, error)
}

// SinkFunc adapts a plain function to the sink interface (Rust impl
// ValidationSink for F: FnMut(&ValidationFinding) -> Result<...>).
type SinkFunc func(*ValidationFinding) (ValidationSinkControl, error)

// Finding implements ValidationSink.
func (f SinkFunc) Finding(finding *ValidationFinding) (ValidationSinkControl, error) {
	if f == nil {
		return SinkContinue, nil
	}
	return f(finding)
}
