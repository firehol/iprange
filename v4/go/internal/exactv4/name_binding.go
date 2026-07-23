package exactv4

import (
	"crypto/sha256"
	"math"
)

const basenameCommitmentDomain = "IPR4NAME"

type basenameEncoding uint16

const (
	basenamePOSIXBytes   basenameEncoding = 1
	basenameWindowsUTF16 basenameEncoding = 2
)

type basenameBindingErrorCode uint8

const (
	basenameBindingEmpty basenameBindingErrorCode = iota + 1
	basenameBindingTooLong
	basenameBindingInvalidPOSIXComponent
	basenameBindingInvalidWindowsComponent
	basenameBindingInvalidUTF16
	basenameBindingInvalidEncoding
)

type basenameBindingError struct {
	code basenameBindingErrorCode
}

func (e *basenameBindingError) Error() string { return "exact v4 basename binding failed" }

var (
	errBasenameEmpty                   = &basenameBindingError{code: basenameBindingEmpty}
	errBasenameTooLong                 = &basenameBindingError{code: basenameBindingTooLong}
	errBasenameInvalidPOSIXComponent   = &basenameBindingError{code: basenameBindingInvalidPOSIXComponent}
	errBasenameInvalidWindowsComponent = &basenameBindingError{code: basenameBindingInvalidWindowsComponent}
	errBasenameInvalidUTF16            = &basenameBindingError{code: basenameBindingInvalidUTF16}
	errBasenameInvalidEncoding         = &basenameBindingError{code: basenameBindingInvalidEncoding}
)

func basenameCommitment(encoding basenameEncoding, name []byte) ([32]byte, *basenameBindingError) {
	if len(name) == 0 {
		return [32]byte{}, errBasenameEmpty
	}
	if uint64(len(name)) > math.MaxUint32 {
		return [32]byte{}, errBasenameTooLong
	}
	switch encoding {
	case basenamePOSIXBytes:
		if err := validatePOSIXBasename(name); err != nil {
			return [32]byte{}, err
		}
	case basenameWindowsUTF16:
		if err := validateWindowsUTF16LEBasename(name); err != nil {
			return [32]byte{}, err
		}
	default:
		return [32]byte{}, errBasenameInvalidEncoding
	}

	var prefix [14]byte
	copy(prefix[:8], basenameCommitmentDomain)
	putU16(prefix[:], 8, uint16(encoding))
	putU32(prefix[:], 10, uint32(len(name)))
	hasher := sha256.New()
	_, _ = hasher.Write(prefix[:])
	_, _ = hasher.Write(name)
	var commitment [32]byte
	hasher.Sum(commitment[:0])
	return commitment, nil
}

func validatePOSIXBasename(name []byte) *basenameBindingError {
	if len(name) == 1 && name[0] == '.' || len(name) == 2 && name[0] == '.' && name[1] == '.' {
		return errBasenameInvalidPOSIXComponent
	}
	for _, value := range name {
		if value == 0 || value == '/' {
			return errBasenameInvalidPOSIXComponent
		}
	}
	return nil
}

func validateWindowsUTF16LEBasename(name []byte) *basenameBindingError {
	if len(name)%2 != 0 {
		return errBasenameInvalidUTF16
	}
	for offset := 0; offset < len(name); offset += 2 {
		unit := uint16(name[offset]) | uint16(name[offset+1])<<8
		if unit == 0 || unit == '/' || unit == '\\' {
			return errBasenameInvalidWindowsComponent
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			offset += 2
			if offset >= len(name) {
				return errBasenameInvalidUTF16
			}
			low := uint16(name[offset]) | uint16(name[offset+1])<<8
			if low < 0xdc00 || low > 0xdfff {
				return errBasenameInvalidUTF16
			}
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			return errBasenameInvalidUTF16
		}
	}
	return nil
}
