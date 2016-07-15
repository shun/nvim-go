// Copyright 2016 Koichi Shiraishi. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package commands

import (
	"nvim-go/context"

	"github.com/neovim-go/vim"
	"github.com/neovim-go/vim/plugin"
)

type Commands struct {
	v *vim.Vim
	p *vim.Pipeline

	ctxt    *context.Context
	errlist map[string][]*vim.QuickfixError
}

func NewCommands(v *vim.Vim) *Commands {
	return &Commands{
		v:       v,
		ctxt:    new(context.Context),
		errlist: make(map[string][]*vim.QuickfixError),
	}
}

func Register(p *plugin.Plugin) {
	c := NewCommands(p.Vim)

	// Register command and function
	p.HandleCommand(&plugin.CommandOptions{Name: "Gobuild", Bang: true, Eval: "[getcwd(), expand('%:p:h')]"}, c.cmdBuild)
	p.HandleCommand(&plugin.CommandOptions{Name: "Godef", Eval: "expand('%:p:h')"}, cmdDef)
	p.HandleCommand(&plugin.CommandOptions{Name: "Gofmt", Eval: "expand('%:p:h')"}, c.cmdFmt)
	p.HandleCommand(&plugin.CommandOptions{Name: "GoGenerateTest", NArgs: "*", Complete: "file", Eval: "expand('%:p:h')"}, c.cmdGenerateTest)
	p.HandleFunction(&plugin.FunctionOptions{Name: "GoGuru", Eval: "[getcwd(), expand('%:p'), &modified, line2byte(line('.')) + (col('.')-2)]"}, c.funcGuru)
	p.HandleCommand(&plugin.CommandOptions{Name: "GoIferr", Eval: "expand('%:p')"}, c.cmdIferr)
	p.HandleCommand(&plugin.CommandOptions{Name: "Gometalinter", Eval: "getcwd()"}, c.cmdMetalinter)
	p.HandleCommand(&plugin.CommandOptions{Name: "Gorename", NArgs: "?", Bang: true, Eval: "[getcwd(), expand('%:p'), expand('<cword>')]"}, c.cmdRename)
	p.HandleCommand(&plugin.CommandOptions{Name: "Gorun", NArgs: "*", Eval: "expand('%:p')"}, c.cmdRun)
	p.HandleCommand(&plugin.CommandOptions{Name: "Gotest", NArgs: "*", Eval: "expand('%:p:h')"}, c.cmdTest)
	p.HandleCommand(&plugin.CommandOptions{Name: "GoTestSwitch", Eval: "[getcwd(), expand('%:p')]"}, c.cmdTestSwitch)

	// for debug
	p.HandleCommand(&plugin.CommandOptions{Name: "GoByteOffset", Range: "%", Eval: "expand('%:p')"}, c.cmdByteOffset)
	p.HandleCommand(&plugin.CommandOptions{Name: "GoBuffers"}, c.cmdBuffers)
	p.HandleCommand(&plugin.CommandOptions{Name: "GoWindows"}, c.cmdWindows)
	p.HandleCommand(&plugin.CommandOptions{Name: "GoTabpages"}, c.cmdTabpagas)
}
