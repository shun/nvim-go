// Copyright 2016 Koichi Shiraishi. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package autocmd

import (
	"path/filepath"
	"runtime"

	"nvim-go/config"

	"github.com/neovim-go/vim"
)

type bufWritePreEval struct {
	Cwd  string `msgpack:",array"`
	File string
}

func (a *Autocmd) bufWritePre(v *vim.Vim, eval *bufWritePreEval) {
	a.bufWritePreChan = make(chan error, runtime.NumCPU())

	dir := filepath.Dir(eval.File)

	// Iferr need execute before Fmt function because that function calls "noautocmd write"
	// Also do not use goroutine.
	if config.IferrAutosave {
		err := a.c.Iferr(eval.File)
		if err != nil {
			return
		}
	}

	if config.FmtAutosave {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.bufWritePreChan <- a.c.Fmt(dir)
		}()
	}

	a.wg.Wait()
}
