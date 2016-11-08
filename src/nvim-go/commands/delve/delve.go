// Copyright 2016 The nvim-go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package delve

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"nvim-go/context"
	"nvim-go/nvimutil"
	"nvim-go/pathutil"

	delveapi "github.com/derekparker/delve/service/api"
	delverpc2 "github.com/derekparker/delve/service/rpc2"
	delveterm "github.com/derekparker/delve/terminal"
	"github.com/neovim/go-client/nvim"
	"github.com/pkg/errors"
)

const (
	defaultAddr = "localhost:41222" // d:4 l:12 v:22
	pkgDelve    = "Delve"
)

// Delve represents a delve client.
type Delve struct {
	v *nvim.Nvim
	p *nvim.Pipeline

	ctxt *context.Context

	server     *exec.Cmd
	client     *delverpc2.RPCClient
	term       *delveterm.Term
	debugger   *delveterm.Commands
	processPid int
	serverOut  bytes.Buffer
	serverErr  bytes.Buffer

	channelID int

	Locals []delveapi.Variable

	BufferContext
	SignContext
}

// BufferContext represents a each debug information buffers.
type BufferContext struct {
	cb     nvim.Buffer
	cw     nvim.Window
	buffer map[string]*nvimutil.Buf
}

// SignContext represents a breakpoint and program counter sign.
type SignContext struct {
	bpSign map[int]*nvimutil.Sign // map[breakPoint.id]*nvim.Sign
	pcSign *nvimutil.Sign
}

// NewDelve represents a delve client interface.
func NewDelve(v *nvim.Nvim, ctxt *context.Context) *Delve {
	return &Delve{
		v:    v,
		ctxt: ctxt,
	}
}

// setupDelve setup the delve client. Separate the NewDelveClient() function.
// caused by neovim-go can't call the rpc2.NewClient?
func (d *Delve) setupDelve(v *nvim.Nvim, addr string) error {
	if !strings.Contains(addr, ":") {
		addr = "localhost:" + addr
	}
	d.client = delverpc2.NewClient(addr)           // *rpc2.RPCClient
	d.term = delveterm.New(d.client, nil)          // *terminal.Term
	d.debugger = delveterm.DebugCommands(d.client) // *terminal.Commands
	d.processPid = d.client.ProcessPid()           // int
	if d.processPid == 0 {
		return errors.New("Cannot setup delve server")
	}

	return nil
}

// ----------------------------------------------------------------------------
// delveEval

// delveEval represent a setup delve server commands Eval args.
type delveEval struct {
	Cwd string `msgpack:",array"`
	Dir string
}

// ----------------------------------------------------------------------------
// debug

func (d *Delve) cmdDebug(v *nvim.Nvim, eval *delveEval) {
	go d.debug(v, eval)
}

// debug setup the debugging with "dlv debug".
// TODO(zchee): If failed debug(build), even create each buffers.
func (d *Delve) debug(v *nvim.Nvim, eval *delveEval) error {
	d.p = d.v.NewPipeline()

	d.ctxt = new(context.Context)
	defer d.ctxt.SetContext(eval.Cwd)()

	rootDir := pathutil.FindVCSRoot(eval.Dir)
	srcPath := filepath.Join(os.Getenv("GOPATH"), "src") + string(filepath.Separator)
	path := filepath.Clean(strings.TrimPrefix(rootDir, srcPath))

	if err := d.startServer("debug", path, defaultAddr); err != nil {
		nvimutil.ErrorWrap(v, err)
	}
	defer d.waitServer(v, defaultAddr)

	return d.createDebugBuffer()
}

// ----------------------------------------------------------------------------
// connect

func (d *Delve) cmdConnect(v *nvim.Nvim, args []string, eval *delveEval) {
	go d.connect(v, args, eval)
}

// connect connect to dlv headless server.
// This command useful for debug the Google Application Engine for Go.
func (d *Delve) connect(v *nvim.Nvim, args []string, eval *delveEval) error {
	d.p = d.v.NewPipeline()

	d.ctxt = new(context.Context)
	defer d.ctxt.SetContext(eval.Cwd)()

	rootDir := pathutil.FindVCSRoot(eval.Dir)
	srcPath := filepath.Join(os.Getenv("GOPATH"), "src") + string(filepath.Separator)
	path := filepath.Clean(strings.TrimPrefix(rootDir, srcPath))

	addr := args[0]
	if !strings.Contains(addr, ":") {
		addr = "localhost:" + addr
	}
	if err := d.startServer("connect", path, addr); err != nil {
		nvimutil.ErrorWrap(v, err)
	}
	defer d.waitServer(v, addr)

	return d.createDebugBuffer()
}

// ----------------------------------------------------------------------------
// break(breakpoint)

// breakpointEval represent a breakpoint commands Eval args.
type breakpointEval struct {
	File string `msgpack:",array"`
}

func (d *Delve) cmdBreakpoint(v *nvim.Nvim, args []string, eval *breakpointEval) {
	go d.breakpoint(v, args, eval)
}

// parseArgs parses the "DlvBreak" command args.
func (d *Delve) parseArgs(v *nvim.Nvim, args []string, eval *breakpointEval) (*delveapi.Breakpoint, error) {
	var bpInfo *delveapi.Breakpoint

	// Ref: https://github.com/derekparker/delve/blob/master/Documentation/cli/locspec.md
	switch len(args) {
	case 0:
		cursor, err := v.WindowCursor(d.cw)
		if err != nil {
			return nil, err
		}

		bpInfo = &delveapi.Breakpoint{
			File: eval.File,
			Line: cursor[0],
		}
	case 1:
		// FIXME(zchee): more elegant way
		splitargs := strings.Split(args[0], ".")
		splitargs[1] = fmt.Sprintf("%s%s", strings.ToUpper(splitargs[1][:1]), splitargs[1][1:])
		name := strings.Join(splitargs, "")

		bpInfo = &delveapi.Breakpoint{
			Name:         name,
			FunctionName: args[0],
		}
	// TODO(zchee): Now support function only.
	default:
		return nil, errors.Wrap(errors.New("Too many arguments"), pkgDelve)
	}

	return bpInfo, nil
}

// breakpoint sets a breakpoint, and sets marker to Nvim sign area.
// Note that 'break' name is reverved Go language spec.
func (d *Delve) breakpoint(v *nvim.Nvim, args []string, eval *breakpointEval) error {
	bpInfo, err := d.parseArgs(v, args, eval)
	if err != nil {
		nvimutil.ErrorWrap(v, err)
	}

	if d.bpSign == nil {
		d.bpSign = make(map[int]*nvimutil.Sign)
	}

	bp, err := d.client.CreateBreakpoint(bpInfo) // *delveapi.Breakpoint
	if err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}

	d.bpSign[bp.ID], err = nvimutil.NewSign(v, "delve_bp", nvimutil.BreakpointSymbol, "delveBreakpointSign", "") // *nvim.Sign
	if err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}
	d.bpSign[bp.ID].Place(v, bp.ID, bp.Line, bp.File, false)

	filename := pathutil.ShortFilePath(bp.File, filepath.Dir(eval.File))
	msg := fmt.Sprintf("Breakpoint %d set at %#v for %s() %s:%d", bp.ID, bp.Addr, bp.FunctionName, filename, bp.Line)
	if err := d.printTerminal("break "+bp.FunctionName, nvimutil.StrToByteSlice(msg)); err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}

	return nil
}

// ----------------------------------------------------------------------------
// continue

// breakpointEval represent a breakpoint commands Eval args.
type continueEval struct {
	Dir string `msgpack:",array"`
}

func (d *Delve) cmdContinue(v *nvim.Nvim, eval *continueEval) {
	go d.cont(v, eval)
}

// cont sends the 'continue' signals to the delve headless server, and update
// sign marker to current stopping position.
// Note that 'continue' name is reverved Go language spec.
func (d *Delve) cont(v *nvim.Nvim, eval *continueEval) error {
	stateCh := d.client.Continue()
	state := <-stateCh
	if state == nil || state.Exited {
		return nvimutil.ErrorWrap(v, errors.Wrap(state.Err, pkgDelve))
	}

	cThread := state.CurrentThread

	go func() {
		goroutines, err := d.client.ListGoroutines()
		if err != nil {
			nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
			return
		}
		d.printContext(eval.Dir, cThread, goroutines)
	}()

	go d.pcSign.Place(v, cThread.ID, cThread.Line, cThread.File, true)

	go func() {
		if err := v.SetWindowCursor(d.cw, [2]int{cThread.Line, 0}); err != nil {
			nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
			return
		}
		if err := v.Command("silent normal zz"); err != nil {
			nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
			return
		}
	}()

	var msg []byte
	if hitCount, ok := cThread.Breakpoint.HitCount[strconv.Itoa(cThread.GoroutineID)]; ok {
		msg = []byte(
			fmt.Sprintf("> %s() %s:%d (hits goroutine(%d):%d total:%d) (PC: %#v)",
				cThread.Function.Name,
				pathutil.ShortFilePath(cThread.File, eval.Dir),
				cThread.Line,
				cThread.GoroutineID,
				hitCount,
				cThread.Breakpoint.TotalHitCount,
				cThread.PC))
	} else {
		msg = []byte(
			fmt.Sprintf("> %s() %s:%d (hits total:%d) (PC: %#v)",
				cThread.Function.Name,
				pathutil.ShortFilePath(cThread.File, eval.Dir),
				cThread.Line,
				cThread.Breakpoint.TotalHitCount,
				cThread.PC))
	}
	return d.printTerminal("continue", msg)
}

// ----------------------------------------------------------------------------
// next

// breakpointEval represent a breakpoint commands Eval args.
type nextEval struct {
	Dir string `msgpack:",array"`
}

func (d *Delve) cmdNext(v *nvim.Nvim, eval *nextEval) {
	go d.next(v, eval)
}

// next sends the 'next' signals to the delve headless server, and update sign
// marker to current stopping position.
func (d *Delve) next(v *nvim.Nvim, eval *nextEval) error {
	state, err := d.client.Next()
	if err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}

	cThread := state.CurrentThread

	go func() {
		goroutines, err := d.client.ListGoroutines()
		if err != nil {
			nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
			return
		}
		d.printContext(eval.Dir, cThread, goroutines)
	}()

	go d.pcSign.Place(v, cThread.ID, cThread.Line, cThread.File, true)

	go func() {
		if err := v.SetWindowCursor(d.cw, [2]int{cThread.Line, 0}); err != nil {
			nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
			return
		}
		if err := v.Command("silent normal zz"); err != nil {
			nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
			return
		}
	}()

	msg := []byte(
		fmt.Sprintf("> %s() %s:%d goroutine(%d) (PC: %d)",
			cThread.Function.Name,
			pathutil.ShortFilePath(cThread.File, eval.Dir),
			cThread.Line,
			cThread.GoroutineID,
			cThread.PC))
	return d.printTerminal("next", msg)
}

// ----------------------------------------------------------------------------
// restart

func (d *Delve) cmdRestart(v *nvim.Nvim) {
	go d.restart(v)
}

func (d *Delve) restart(v *nvim.Nvim) error {
	err := d.client.Restart()
	if err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}

	d.processPid = d.client.ProcessPid()
	return d.printTerminal("restart", []byte(fmt.Sprintf("Process restarted with PID %d", d.processPid)))
}

// ----------------------------------------------------------------------------
// state

func (d *Delve) cmdState(v *nvim.Nvim) {
	go d.state(v)
}

func (d *Delve) state(v *nvim.Nvim) error {
	state, err := d.client.GetState()
	if err != nil {
		return errors.Wrap(err, pkgDelve)
	}
	printDebug("state: %+v\n", state)
	return nil
}

// ----------------------------------------------------------------------------
// stdin

func (d *Delve) cmdStdin(v *nvim.Nvim) {
	go d.stdin(v)
}

// stdin sends the users input command to the internal delve terminal.
// vim input() function args:
//  input({prompt} [, {text} [, {completion}]])
// More information of input() funciton and word completion are
//  :help input()
//  :help command-completion-custom
func (d *Delve) stdin(v *nvim.Nvim) error {
	var stdin interface{}
	err := v.Call("input", &stdin, "(dlv) ", "")
	if err != nil {
		return nil
	}

	// Create the connected pair of *os.Files and replace os.Stdout.
	// delve terminal package return to stdout only.
	r, w, _ := os.Pipe() // *os.File
	saveStdout := os.Stdout
	os.Stdout = w

	cmd := strings.SplitN(stdin.(string), " ", 2)
	var args string
	if len(cmd) == 2 {
		args = cmd[1]
	}

	err = d.debugger.Call(cmd[0], args, d.term)
	if err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}

	// Close the w file and restore os.Stdout to original.
	w.Close()
	os.Stdout = saveStdout

	// Read all the lines of r file.
	out, err := ioutil.ReadAll(r)
	if err != nil {
		return nvimutil.ErrorWrap(v, errors.Wrap(err, pkgDelve))
	}

	return d.printTerminal(stdin.(string), out)
}

// ----------------------------------------------------------------------------
// command line completion

// FunctionsCompletion return the debug target functions with filtering "main".
func (d *Delve) FunctionsCompletion(v *nvim.Nvim) ([]string, error) {
	funcs, err := d.client.ListFunctions("main")
	if err != nil {
		return []string{}, err
	}

	return funcs, nil
}
