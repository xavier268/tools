// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package span

import (
	"fmt"
	"go/token"

	"golang.org/x/tools/internal/lsp/bug"
)

// Range represents a source code range in token.Pos form.
// It also carries the FileSet that produced the positions, so that it is
// self contained.
type Range struct {
	Start token.Pos
	End   token.Pos

	// TokFile may be nil if Start or End is invalid.
	// TODO: Eventually we should guarantee that it is non-nil.
	TokFile *token.File
}

// TokenConverter converts between offsets and (col, row) using a token.File.
//
// TODO(rfindley): eliminate TokenConverter in favor of just operating on
// token.File.
type TokenConverter struct {
	// TokFile is exported for invariant checking; it may be nil in the case of
	// an invalid converter.
	TokFile *token.File
}

// NewRange creates a new Range from a FileSet and two positions.
// To represent a point pass a 0 as the end pos.
func NewRange(fset *token.FileSet, start, end token.Pos) Range {
	file := fset.File(start)
	if file == nil {
		bug.Reportf("nil file")
	}
	return Range{
		Start:   start,
		End:     end,
		TokFile: file,
	}
}

// NewTokenConverter returns an implementation of Converter backed by a
// token.File.
func NewTokenConverter(f *token.File) *TokenConverter {
	if f == nil {
		bug.Reportf("nil file")
	}
	return &TokenConverter{TokFile: f}
}

// NewContentConverter returns an implementation of Converter for the
// given file content.
func NewContentConverter(filename string, content []byte) *TokenConverter {
	fset := token.NewFileSet()
	f := fset.AddFile(filename, -1, len(content))
	f.SetLinesForContent(content)
	return NewTokenConverter(f)
}

// IsPoint returns true if the range represents a single point.
func (r Range) IsPoint() bool {
	return r.Start == r.End
}

// Span converts a Range to a Span that represents the Range.
// It will fill in all the members of the Span, calculating the line and column
// information.
func (r Range) Span() (Span, error) {
	return FileSpan(r.TokFile, NewTokenConverter(r.TokFile), r.Start, r.End)
}

// FileSpan returns a span within tok, using converter to translate between
// offsets and positions.
//
// If non-nil, the converter must be a converter for the source file pointed to
// by start, after accounting for //line directives, as it will be used to
// compute offsets for the resulting Span.
func FileSpan(tok *token.File, converter *TokenConverter, start, end token.Pos) (Span, error) {
	if !start.IsValid() {
		return Span{}, fmt.Errorf("start pos is not valid")
	}
	if tok == nil {
		return Span{}, bug.Errorf("missing file association") // should never get here with a nil file
	}
	var s Span
	var err error
	var startFilename string
	startFilename, s.v.Start.Line, s.v.Start.Column, err = position(tok, start)
	if err != nil {
		return Span{}, err
	}
	s.v.URI = URIFromPath(startFilename)
	if end.IsValid() {
		var endFilename string
		endFilename, s.v.End.Line, s.v.End.Column, err = position(tok, end)
		if err != nil {
			return Span{}, err
		}
		// In the presence of line directives, a single File can have sections from
		// multiple file names.
		if endFilename != startFilename {
			return Span{}, fmt.Errorf("span begins in file %q but ends in %q", startFilename, endFilename)
		}
	}
	s.v.Start.clean()
	s.v.End.clean()
	s.v.clean()
	if converter == nil {
		converter = &TokenConverter{tok}
	}
	if startFilename != converter.TokFile.Name() {
		return Span{}, bug.Errorf("must supply Converter for file %q containing lines from %q", tok.Name(), startFilename)
	}
	return s.WithOffset(converter)
}

func position(f *token.File, pos token.Pos) (string, int, int, error) {
	off, err := offset(f, pos)
	if err != nil {
		return "", 0, 0, err
	}
	return positionFromOffset(f, off)
}

func positionFromOffset(f *token.File, offset int) (string, int, int, error) {
	if offset > f.Size() {
		return "", 0, 0, fmt.Errorf("offset %v is past the end of the file %v", offset, f.Size())
	}
	pos := f.Pos(offset)
	p := f.Position(pos)
	// TODO(golang/go#41029): Consider returning line, column instead of line+1, 1 if
	// the file's last character is not a newline.
	if offset == f.Size() {
		return p.Filename, p.Line + 1, 1, nil
	}
	return p.Filename, p.Line, p.Column, nil
}

// offset is a copy of the Offset function in go/token, but with the adjustment
// that it does not panic on invalid positions.
func offset(f *token.File, pos token.Pos) (int, error) {
	if int(pos) < f.Base() || int(pos) > f.Base()+f.Size() {
		return 0, fmt.Errorf("invalid pos: %d not in [%d, %d]", pos, f.Base(), f.Base()+f.Size())
	}
	return int(pos) - f.Base(), nil
}

// Range converts a Span to a Range that represents the Span for the supplied
// File.
func (s Span) Range(converter *TokenConverter) (Range, error) {
	s, err := s.WithOffset(converter)
	if err != nil {
		return Range{}, err
	}
	file := converter.TokFile
	// go/token will panic if the offset is larger than the file's size,
	// so check here to avoid panicking.
	if s.Start().Offset() > file.Size() {
		return Range{}, bug.Errorf("start offset %v is past the end of the file %v", s.Start(), file.Size())
	}
	if s.End().Offset() > file.Size() {
		return Range{}, bug.Errorf("end offset %v is past the end of the file %v", s.End(), file.Size())
	}
	return Range{
		Start:   file.Pos(s.Start().Offset()),
		End:     file.Pos(s.End().Offset()),
		TokFile: file,
	}, nil
}

func (l *TokenConverter) ToPosition(offset int) (int, int, error) {
	_, line, col, err := positionFromOffset(l.TokFile, offset)
	return line, col, err
}

func (l *TokenConverter) ToOffset(line, col int) (int, error) {
	if line < 0 {
		return -1, fmt.Errorf("line is not valid")
	}
	lineMax := l.TokFile.LineCount() + 1
	if line > lineMax {
		return -1, fmt.Errorf("line is beyond end of file %v", lineMax)
	} else if line == lineMax {
		if col > 1 {
			return -1, fmt.Errorf("column is beyond end of file")
		}
		// at the end of the file, allowing for a trailing eol
		return l.TokFile.Size(), nil
	}
	pos := l.TokFile.LineStart(line)
	if !pos.IsValid() {
		return -1, fmt.Errorf("line is not in file")
	}
	// we assume that column is in bytes here, and that the first byte of a
	// line is at column 1
	pos += token.Pos(col - 1)
	return offset(l.TokFile, pos)
}
