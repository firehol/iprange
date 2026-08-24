package validation

// Validators still to arrive, in the Rust validate_selected order
// (validation.rs:460): the no-op stub keeps the composition visible and
// the sweep executable while slice E lands.

// validateStructure runs the structure table validators (Rust
// structure::validate; slice E).
func validateStructure(*context) error { return nil }
