// Copyright 2016 Koichi Shiraishi. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package nvim

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/garyburd/neovim-go/vim"
	"github.com/juju/errors"
)

const pkgBuffer = "nvim.buffer"

type Buffer struct {
	v *vim.Vim
	p *vim.Pipeline

	BufferContext
	WindowContext
	TabpageContext
}

type BufferContext struct {
	Buffer vim.Buffer

	Name     string
	Filetype string
	Bufnr    int
	Mode     string
}

type WindowContext struct {
	Window vim.Window
}

type TabpageContext struct {
	Tabpage vim.Tabpage
}

type VimOption int

const (
	BufferOption VimOption = iota
	BufferVar
	WindowOption
	WindowVar
)

func NewBuffer(v *vim.Vim, name, filetype, mode string, option map[VimOption]map[string]interface{}) *Buffer {
	b := &Buffer{
		v: v,
		p: v.NewPipeline(),
		BufferContext: BufferContext{
			Name:     name,
			Filetype: filetype,
			Mode:     mode,
		},
	}

	err := b.v.Command(fmt.Sprintf("silent %s %s", b.Mode, b.Name))
	if err != nil {
		errors.Annotate(err, pkgBuffer)
		return nil
	}

	b.p.CurrentBuffer(&b.Buffer)
	b.p.CurrentWindow(&b.Window)
	b.p.CurrentTabpage(&b.Tabpage)
	if err := b.p.Wait(); err != nil {
		errors.Annotate(err, pkgBuffer)
		return nil
	}

	b.p.BufferNumber(b.Buffer, &b.Bufnr)

	if option != nil {
		if option[BufferOption] != nil {
			for k, op := range option[BufferOption] {
				b.p.SetBufferOption(b.Buffer, k, op)
			}
		}
		if option[BufferVar] != nil {
			for k, op := range option[BufferVar] {
				b.p.SetBufferVar(b.Buffer, k, op, nil)
			}
		}
		if option[WindowOption] != nil {
			for k, op := range option[WindowOption] {
				b.p.SetBufferOption(b.Buffer, k, op)
			}
		}
		if option[WindowVar] != nil {
			for k, op := range option[WindowVar] {
				b.p.SetWindowVar(b.Window, k, op, nil)
			}
		}
	}

	if !strings.Contains(b.Name, ".") {
		b.p.Command(fmt.Sprintf("runtime! syntax/%s.vim", filetype))
	}
	b.p.Wait()

	return b
}

// UpdateSyntax updates the syntax highlight of the buffer.
func (b *Buffer) UpdateSyntax(syntax string) {
	if b.Name != "" {
		b.v.SetBufferName(b.Buffer, b.Name)
	}

	if syntax == "" {
		var filetype interface{}
		b.v.BufferOption(b.Buffer, "filetype", &filetype)
		syntax = fmt.Sprintf("%s", filetype)
	}

	b.v.Command(fmt.Sprintf("runtime! syntax/%s.vim", syntax))
}

// SetBufferMapping sets buffer local mapping.
// 'mapping' arg: [key]{destination}
func (b *Buffer) SetLocalMapping(mode string, mapping map[string]string) error {
	if mapping != nil {
		cwin, err := b.v.CurrentWindow()
		if err != nil {
			return errors.Annotate(err, "nvim/buffer.SetMapping")
		}

		b.p.SetCurrentWindow(b.Window)
		defer b.v.SetCurrentWindow(cwin)

		for k, v := range mapping {
			b.p.Command(fmt.Sprintf("silent %s <buffer><silent>%s %s", mode, k, v))
		}
	}

	return b.p.Wait()
}

// lineCount counts the Neovim buffer line count and check whether 1 count,
// Because new(empty) buffer and one line buffer are both 1 count.
func (b *Buffer) lineCount() (int, error) {
	lineCount, err := b.v.BufferLineCount(b.Buffer)
	if err != nil {
		return 0, errors.Annotate(err, pkgBuffer)
	}

	if lineCount == 1 {
		line, err := b.v.CurrentLine()
		if err != nil {
			return 0, errors.Annotate(err, pkgBuffer)
		}
		// Set 0 to lineCount if buffer is empty
		if len(line) == 0 {
			lineCount = 0
		}
	}

	return lineCount, nil
}

// Write appends the contents of p to the Neovim buffer.
func (b *Buffer) Write(p []byte) error {
	lineCount, err := b.lineCount()
	if err != nil {
		return errors.Annotate(err, pkgBuffer)
	}

	buf := bytes.NewBuffer(p)

	return b.v.SetBufferLines(b.Buffer, lineCount, -1, true, ToBufferLines(buf.Bytes()))
}

// WriteString appends the contents of s to the Neovim buffer.
func (b *Buffer) WriteString(s string) error {
	lineCount, err := b.lineCount()
	if err != nil {
		return errors.Annotate(err, pkgBuffer)
	}

	buf := bytes.NewBufferString(s)

	return b.v.SetBufferLines(b.Buffer, lineCount, -1, true, ToBufferLines(buf.Bytes()))
}

// Truncate discards all but the first n unread bytes from the
// Neovim buffer but continues to use the same allocated storage.
func (b *Buffer) Truncate(n int) {
	b.v.SetBufferLines(b.Buffer, n, -1, true, [][]byte{})
}

// Reset resets the Neovim buffer to be empty,
// but it retains the underlying storage for use by future writes.
// Reset is the same as Truncate(0).
func (b *Buffer) Reset() { b.Truncate(0) }

// Contains reports whether buffer list is within b.
func BufContains(v *vim.Vim, b vim.Buffer) bool {
	bufs, _ := v.Buffers()
	for _, buf := range bufs {
		if buf == b {
			return true
		}
	}
	return false
}

// BufExists reports whether buffer list is within bufnr use vim bufexists function.
func BufExists(v *vim.Vim, bufnr int) bool {
	var res interface{}
	v.Call("bufexists", &res, bufnr)

	return res.(int) != 0
}

// Modifiable sets modifiable to true,
// The returned function restores modifiable to false.
func Modifiable(v *vim.Vim, b vim.Buffer) func() {
	v.SetBufferOption(b, BufOptionModifiable, true)

	return func() {
		v.SetBufferOption(b, BufOptionModifiable, false)
	}
}

// ToByteSlice converts the 2D buffer data to sigle byte slice.
func ToByteSlice(byt [][]byte) []byte { return bytes.Join(byt, []byte{'\n'}) }

// ToBufferLines converts the byte slice to the 2D slice of Neovim buffer data-type.
func ToBufferLines(byt []byte) [][]byte { return bytes.Split(byt, []byte{'\n'}) }

// ByteOffset calculates the byte-offset of current cursor position.
func ByteOffset(v *vim.Vim, b vim.Buffer, w vim.Window) (int, error) {
	cursor, _ := v.WindowCursor(w)
	byteBuf, _ := v.BufferLines(b, 0, -1, true)

	if cursor[0] == 1 {
		return (1 + (cursor[1] - 1)), nil
	}

	var offset int
	line := 1
	for _, buf := range byteBuf {
		if line == cursor[0] {
			offset++
			break
		}
		offset += (binary.Size(buf) + 1)
		line++
	}

	return (offset + (cursor[1] - 1)), nil
}

// ByteOffsetPipe calculates the byte-offset of current cursor position uses vim.Pipeline.
func ByteOffsetPipe(p *vim.Pipeline, b vim.Buffer, w vim.Window) (int, error) {
	var cursor [2]int
	p.WindowCursor(w, &cursor)

	var byteBuf [][]byte
	p.BufferLines(b, 0, -1, true, &byteBuf)

	if err := p.Wait(); err != nil {
		return 0, errors.Annotate(err, "nvim/buffer.ByteOffsetPipe")
	}

	if cursor[0] == 1 {
		return (1 + (cursor[1] - 1)), nil
	}

	offset := 0
	line := 1
	for _, buf := range byteBuf {
		if line == cursor[0] {
			offset++
			break
		}
		offset += (binary.Size(buf) + 1)
		line++
	}

	return (offset + (cursor[1] - 1)), nil
}
