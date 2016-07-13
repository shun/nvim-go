// Copyright 2016 Koichi Shiraishi. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"time"

	"nvim-go/config"
	"nvim-go/context"
	"nvim-go/nvim"
	"nvim-go/nvim/profile"
	"nvim-go/nvim/quickfix"

	"github.com/neovim-go/vim"
)

// CmdBuildEval struct type for Eval of GoBuild command.
type CmdBuildEval struct {
	Cwd string `msgpack:",array"`
	Dir string
}

func cmdBuild(v *vim.Vim, bang bool, eval *CmdBuildEval) {
	go Build(v, bang, eval)
}

// Build builds the current buffer's package use compile tool that
// determined from the directory structure.
func Build(v *vim.Vim, bang bool, eval *CmdBuildEval) error {
	defer profile.Start(time.Now(), "GoBuild")
	ctxt := new(context.Context)
	defer ctxt.Build.SetContext(eval.Dir)()

	if !bang {
		bang = config.BuildForce
	}

	cmd, err := compileCmd(ctxt, bang, eval.Cwd)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return nvim.EchoSuccess(v, "GoBuild", fmt.Sprintf("compiler: %s", ctxt.Build.Tool))
	}

	if _, ok := err.(*exec.ExitError); ok {
		w, err := v.CurrentWindow()
		if err != nil {
			return err
		}

		loclist, err := quickfix.ParseError(stderr.Bytes(), eval.Cwd, &ctxt.Build)
		if err != nil {
			return err
		}
		if err := quickfix.SetLoclist(v, loclist); err != nil {
			return err
		}

		return quickfix.OpenLoclist(v, w, loclist, true)
	}

	return err
}

func compileCmd(ctxt *context.Context, bang bool, dir string) (*exec.Cmd, error) {
	cmd := exec.Command(ctxt.Build.Tool)
	args := []string{"build"}

	if len(config.BuildArgs) > 0 {
		args = append(args, config.BuildArgs...)
	}

	switch ctxt.Build.Tool {
	case "go":
		cmd.Dir = dir
		if !bang {
			tmpfile, err := ioutil.TempFile(os.TempDir(), "nvim-go")
			if err != nil {
				return nil, err
			}
			defer os.Remove(tmpfile.Name())
			args = append(args, "-o", tmpfile.Name())
		}
	case "gb":
		cmd.Dir = ctxt.Build.GbProjectDir
	}
	cmd.Args = append(cmd.Args, args...)

	return cmd, nil
}
