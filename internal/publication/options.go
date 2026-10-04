package publication

import "errors"

type Mode string

const (
	Create  Mode = "create"
	Replace Mode = "replace"
	InPlace Mode = "in-place"
)

type Options struct {
	Source string
	Output string
	Mode   Mode
}

var (
	ErrInvalidMode           = errors.New("invalid publication mode")
	ErrInvalidPath           = errors.New("unsafe publication path")
	ErrAlias                 = errors.New("source and output alias")
	ErrBusy                  = errors.New("publication directory is busy")
	ErrUnsupportedFilesystem = errors.New("unqualified publication filesystem")
	ErrCapability            = errors.New("publication capability unavailable")
)
