package validation

// Tree page validation (Rust validation/page.rs): the slotted-header
// inspection with the exact reason mapping and the fixed/variable cell
// layout checks over the format authorities.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// treePageSpec selects the identity of one tree page (Rust
// validation/page.rs TreePageSpec): the branch and leaf types, the aux
// discriminator, and the expected child level when the walk descends.
type treePageSpec struct {
	branchType    byte
	leafType      byte
	aux           uint32
	expectedLevel *uint16
}

// treePageHeader runs the tree-page header inspection and streams the
// classified finding on refusal (Rust page::slotted_header: Header and
// Shape both report PageHeaderInvalid, Born PageBornTxnInvalid, Type
// PageTypeMismatch, Level TreeLevelInvalid). A nil header with a nil
// error means the page was refused as a finding.
func treePageHeader(ctx *context, pageNumber uint32, page []byte, object ValidationObject, spec treePageSpec) (*format.PageHeader, error) {
	header, problem := format.InspectTreeHeader(page, ctx.meta.TxnID, spec.branchType, spec.leafType, spec.aux, spec.expectedLevel)
	if problem != format.TreeHeaderProblemNone {
		if err := ctx.emit(treeHeaderProblemReason(problem), object, &pageNumber, nil, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &header, nil
}

// treeHeaderProblemReason maps one header problem to its reason class
// (Rust page.rs slotted_header match).
func treeHeaderProblemReason(problem format.TreeHeaderProblem) ValidationReason {
	switch problem {
	case format.TreeHeaderProblemBorn:
		return ReasonPageBornTxnInvalid
	case format.TreeHeaderProblemType:
		return ReasonPageTypeMismatch
	case format.TreeHeaderProblemLevel:
		return ReasonTreeLevelInvalid
	default:
		return ReasonPageHeaderInvalid
	}
}

// validateFixedCells checks the fixed-cell layout of one tree page (Rust
// page::validate_fixed_cells).
func validateFixedCells(ctx *context, pageNumber uint32, page []byte, object ValidationObject, header *format.PageHeader, cellLen int) (format.LayoutInspection, bool, error) {
	return validateTreeLayout(ctx, pageNumber, page, object, header, format.FixedLayout(cellLen))
}

// validateVariableCells checks the variable-record layout of one tree
// page (Rust page::validate_variable_cells).
func validateVariableCells(ctx *context, pageNumber uint32, page []byte, object ValidationObject, header *format.PageHeader, minimum, maximum int) (format.LayoutInspection, bool, error) {
	return validateTreeLayout(ctx, pageNumber, page, object, header, format.VariableLayout(minimum, maximum))
}

// validateTreeLayout mirrors page::validate_layout: an invalid layout
// reports PageHeaderInvalid and refuses the page; a nonzero reserved
// region reports PageReservedNonzero and keeps the page (Rust keeps the
// inspection in both cases).
func validateTreeLayout(ctx *context, pageNumber uint32, page []byte, object ValidationObject, header *format.PageHeader, layout format.CellLayout) (format.LayoutInspection, bool, error) {
	inspection, ok := format.InspectLayout(page, header, layout)
	if !ok {
		if err := ctx.emit(ReasonPageHeaderInvalid, object, &pageNumber, nil, nil); err != nil {
			return format.LayoutInspection{}, false, err
		}
		return format.LayoutInspection{}, false, nil
	}
	if inspection.ReservedNonzero {
		if err := ctx.emit(ReasonPageReservedNonzero, object, &pageNumber, nil, nil); err != nil {
			return format.LayoutInspection{}, false, err
		}
	}
	return inspection, true, nil
}
