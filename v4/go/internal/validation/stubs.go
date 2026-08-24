package validation

// Validators still to arrive, in the Rust validate_selected order
// (validation.rs:460): the no-op stubs keep the composition visible and
// the sweep executable while slices C-E land. Each stub names its slice.

// validateStructure runs the structure table validators (Rust
// structure::validate; slice E).
func validateStructure(*context) error { return nil }

// validateMembership runs the membership validators (Rust
// membership::validate; slice D).
func validateMembership(*context) error { return nil }

// validateFreeBitmap runs the free bitmap validator (Rust bitmap::validate
// with Kind::Free; slice D).
func validateFreeBitmap(*context) error { return nil }
