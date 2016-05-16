// Copyright 2016 Koichi Shiraishi. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"

	"nvim-go/nvim"

	delveapi "github.com/derekparker/delve/service/api"
	delverpc2 "github.com/derekparker/delve/service/rpc2"
	delveterminal "github.com/derekparker/delve/terminal"
	"github.com/garyburd/neovim-go/vim"
	"github.com/garyburd/neovim-go/vim/plugin"
)

const addr = "localhost:41222" // d:4 l:12 v:22

var (
	delve  *DelveClient
	server *exec.Cmd

	stdout, stderr bytes.Buffer

	p           *vim.Pipeline
	channelId   int
	baseTabpage vim.Tabpage

	// TODO(zchee): More elegant way.
	src    = &bufferInfo{}
	logs   = &bufferInfo{}
	breaks = &bufferInfo{}
	stacks = &bufferInfo{}
	locals = &bufferInfo{}
)

type bufferInfo struct {
	buffer vim.Buffer
	window vim.Window

	bufnr     interface{}
	linecount int
	name      string
}

// DelveClient represents a delve debugger interface and buffer information.
type DelveClient struct {
	client   *delverpc2.RPCClient
	term     *delveterminal.Term
	debugger *delveterminal.Commands

	addr    string
	procPid int

	buffers     map[vim.Buffer]*bufferInfo
	breakpoints map[int]*delveapi.Breakpoint
	bpSign      map[string]*nvim.Sign
	pcSign      *nvim.Sign
	lastBpId    int
}

// NewDelveClient represents a delve client interface.
func NewDelveClient(addr string) *DelveClient {
	// TODO(zchee): custimizable listen address. Now use constant port.
	// delve can remote debugging of another PC over the http?
	// and can debug any binary in the Docker container?
	return &DelveClient{
		addr: addr,
	}
}

func init() {
	// Launch
	plugin.HandleCommand("DlvStartServer", &plugin.CommandOptions{NArgs: "*", Eval: "[getcwd(), expand('%:p:h')]", Complete: "file"}, cmdDelveStartServer)
	plugin.HandleCommand("DlvStartClient", &plugin.CommandOptions{Eval: "[getcwd(), expand('%:p:h')]"}, delveStartClient)

	// Command
	plugin.HandleCommand("DlvContinue", &plugin.CommandOptions{}, cmdDelveContinue)
	plugin.HandleCommand("DlvNext", &plugin.CommandOptions{}, cmdDelveNext)
	plugin.HandleCommand("DlvRestart", &plugin.CommandOptions{}, cmdDelveRestart)
	plugin.HandleCommand("DlvDisassemble", &plugin.CommandOptions{}, delveDisassemble)
	plugin.HandleCommand("DlvCommand", &plugin.CommandOptions{NArgs: "+"}, cmdDelveCommand)

	// Breokpoint
	plugin.HandleCommand("DlvBreakpoint", &plugin.CommandOptions{NArgs: "+", Complete: "customlist,DelveFunctionList"}, delveBreakpoint)
	plugin.HandleFunction("DelveFunctionList", &plugin.FunctionOptions{}, delveFunctionList)

	// RPC export
	plugin.Handle("DlvContinue", cmdDelveContinue)
	plugin.Handle("DlvNext", cmdDelveNext)
	plugin.Handle("DlvRestart", cmdDelveRestart)
	plugin.Handle("DlvDetach", CmdDelveDetach)

	// Exit
	plugin.HandleCommand("DlvDetach", &plugin.CommandOptions{}, CmdDelveDetach)
	plugin.HandleCommand("DlvKill", &plugin.CommandOptions{}, CmdDelveKill)
}

// cmdBuildEval represent a Dlv commands Eval args.
type cmdDelveEval struct {
	Cwd string `msgpack:",array"`
	Dir string
}

// Wrapper function for commands using goroutine.
//
// The advantage is do not freeze the neovim user interface even if any command resulting the busy state.
// Note may become multistage concurrency processing.
//
//  Neovim rpc call (asynchronous)
//    -> Wrapper function (goroutine)
//      -> Remote plugin internal (goroutine)
//        -> neovim-go/vim.Pipeline (goroutine & chan)
func cmdDelveStartServer(v *vim.Vim, args []string, eval cmdDelveEval) {
	go delveStartServer(v, args, eval)
}
func cmdDelveCommand(v *vim.Vim, args []string) {
	go delveCommand(v, args)
}
func cmdDelveContinue(v *vim.Vim) {
	go delveContinue(v)
}
func cmdDelveNext(v *vim.Vim) {
	go delveNext(v)
}
func cmdDelveRestart(v *vim.Vim) {
	go delveRestart(v)
}
func CmdDelveDetach(v *vim.Vim) {
	go delveDetach(v)
}
func CmdDelveKill(v *vim.Vim) {
	go delveKill()
}

// startServer starts the delve headless server and hijacked stdout & stderr.
func delveStartServer(v *vim.Vim, args []string, eval cmdDelveEval) error {
	bin, err := exec.LookPath("astdump")
	if err != nil {
		return err
	}

	serverArgs := []string{"exec", bin, "--headless=true", "--accept-multiclient=true", "--api-version=2", "--log", "--listen=" + addr}
	server = exec.Command("dlv", serverArgs...)

	server.Stdout = &stdout
	server.Stderr = &stderr

	err = server.Run()
	if err != nil {
		return err
	}

	return nil
}

// dlvStartClient starts the delve client use json-rpc2 protocol.
func delveStartClient(v *vim.Vim, eval cmdDelveEval) error {
	if server == nil {
		return nvim.EchohlErr(v, "Delve", "dlv headless server not running")
	}

	delve = NewDelveClient(addr)
	delve.client = delverpc2.NewClient(addr)
	delve.procPid = delve.client.ProcessPid()
	delve.buffers = make(map[vim.Buffer]*bufferInfo, 5)

	delve.term = delveterminal.New(delve.client, nil)
	delve.debugger = delveterminal.DebugCommands(delve.client)

	channelId, _ = v.ChannelID()
	baseTabpage, _ = v.CurrentTabpage()

	p = v.NewPipeline()
	newBuffer("source", "0tab", 0, "new", src)

	var width, height int
	p.Command("runtime! syntax/go.vim")

	// Define sign for breakpoint hit line.
	// TODO(zchee): Custumizable sign text and highlight group.
	var err error
	delve.pcSign, err = nvim.NewSign(v, "delve_pc", "->", "String", "Search")
	delve.bpSign = map[string]*nvim.Sign{}
	p.Command("sign define delve_bp text=B> texthl=Type")
	p.WindowWidth(src.window, &width)
	p.WindowHeight(src.window, &height)
	if err := p.Wait(); err != nil {
		return err
	}

	// TODO(zchee): Split create buffer section and set buffer option section. Now "delveStartClient" command is slow.
	// 1. Split the current full width buffer to each output buffers.
	// We can't use goroutine because may become different split size and buffer position.
	// neovim (v)split behavior can absolute size?
	// 2. Set buffer option for each output buffer use goroutine.
	newBuffer("stacktrace", "belowright", (width * 2 / 5), "vsplit", stacks)
	newBuffer("breakpoint", "belowright", (height * 1 / 3), "split", breaks)
	newBuffer("locals", "belowright", (height * 1 / 3), "split", locals)
	p.SetCurrentWindow(src.window)
	if err := p.Wait(); err != nil {
		return err
	}
	newBuffer("logs", "belowright", (height * 1 / 3), "split", logs)
	p.SetCurrentWindow(src.window)

	// Gets the default unrecovered-panic breakpoint
	delve.breakpoints = make(map[int]*delveapi.Breakpoint)
	panic, err := delve.client.GetBreakpoint(-1)
	if err != nil {
		return nvim.EchohlErr(v, "Delve", err)
	}
	delve.breakpoints[-1] = panic

	sbp := fmt.Sprintf("Breakpoint %d\n\tPC=%#x func=%s() File=%s:%d (%d)",
		panic.ID,
		panic.Addr,
		panic.FunctionName,
		panic.File,
		panic.Line,
		panic.ID)
	printbp := bytes.NewBufferString(sbp)
	if breaks.linecount, err = printBuffer(v, breaks.buffer, true, bytes.Split(printbp.Bytes(), []byte{'\n'})); err != nil {
		return err
	}
	if err := v.SetWindowCursor(breaks.window, [2]int{breaks.linecount, 0}); err != nil {
		return err
	}

	return p.Wait()
}

func newBuffer(name string, mode string, size int, split string, buf *bufferInfo) error {
	buf.name = name
	p.Command(fmt.Sprintf("silent %s %d%s [delve] %s", mode, size, split, buf.name))
	if err := p.Wait(); err != nil {
		return err
	}

	p.CurrentBuffer(&buf.buffer)
	p.CurrentWindow(&buf.window)
	if err := p.Wait(); err != nil {
		return err
	}

	delve.buffers[buf.buffer] = buf

	p.Eval("bufnr('%')", &buf.bufnr)
	p.SetBufferOption(buf.buffer, "filetype", "delve")
	p.SetBufferOption(buf.buffer, "buftype", "nofile")
	p.SetBufferOption(buf.buffer, "bufhidden", "delete")
	p.SetBufferOption(buf.buffer, "buflisted", false)
	p.SetBufferOption(buf.buffer, "swapfile", false)
	p.SetWindowOption(buf.window, "winfixheight", true)
	if buf.name != "source" {
		p.SetWindowOption(buf.window, "list", false)
		p.SetWindowOption(buf.window, "number", false)
		p.SetWindowOption(buf.window, "relativenumber", false)
	}
	// modifiable lock to buffer.
	p.SetBufferOption(buf.buffer, "modifiable", false)
	if err := p.Wait(); err != nil {
		return err
	}
	// TODO(zchee): Why can't use p.SetBufferOption?
	p.Call("setbufvar", nil, buf.bufnr.(int64), "&colorcolumn", "")

	// TODO(zchee): Move to <Plug> mappnig when releases.
	p.Command(fmt.Sprintf("nnoremap <buffer><silent>c :<C-u>call rpcrequest(%d, 'DlvContinue')<CR>", channelId))
	p.Command(fmt.Sprintf("nnoremap <buffer><silent>n :<C-u>call rpcrequest(%d, 'DlvNext')<CR>", channelId))
	p.Command(fmt.Sprintf("nnoremap <buffer><silent>r :<C-u>call rpcrequest(%d, 'DlvRestart')<CR>", channelId))
	p.Command(fmt.Sprintf("nnoremap <buffer><silent>q :<C-u>call rpcrequest(%d, 'DlvDetach')<CR>", channelId))

	return p.Wait()
}

// delveCommand sends the users input delve subcommand and arguments to the internal launched delve vertual terminal.
func delveCommand(v *vim.Vim, args []string) error {
	// Create the connected pair of *os.Files and replace os.Stdout.
	// delve terminal return to stdout only.
	r, w, _ := os.Pipe() // *os.File
	saveStdout := os.Stdout
	os.Stdout = w

	// First command arguments is delve subcommand.
	// Splits the after arguments with whitespace.
	err := delve.debugger.Call(args[0], strings.Join(args[1:], " "), delve.term)
	if err != nil {
		return err
	}

	// Close the w file and restore os.Stdout to original.
	w.Close()
	os.Stdout = saveStdout

	// Read all the lines of r file and output results to logs buffer.
	out := []byte("(dlv) ")
	result, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	out = append(out, result...)
	logs.linecount, err = printBuffer(v, logs.buffer, true, bytes.Split(bytes.TrimSpace(out), []byte{'\n'}))
	if err != nil {
		return err
	}
	if err := v.SetWindowCursor(logs.window, [2]int{logs.linecount, 0}); err != nil {
		return err
	}

	return nil
}

// ByID sorts breakpoints by ID.
type ByID []*delveapi.Breakpoint

func (a ByID) Len() int           { return len(a) }
func (a ByID) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByID) Less(i, j int) bool { return a[i].ID < a[j].ID }

func delveBreakpoint(v *vim.Vim, args []string) error {
	var bpName string

	switch len(args) {
	case 0:
		return nvim.EchohlErr(v, "Delve", "Invalid argument")
	case 1:
		// TODO(zchee): more elegant way
		bpslice := strings.Split(args[0], ".")
		bpslice[1] = fmt.Sprintf("%s%s", strings.ToUpper(bpslice[1][:1]), bpslice[1][1:])
		bpName = strings.Join(bpslice, "")
	case 2:
		bpName = args[1]
	default:
		return nvim.EchohlErr(v, "Delve", "Too many arguments")
	}

	newbp, err := delve.client.CreateBreakpoint(&delveapi.Breakpoint{
		FunctionName: args[0],
		Name:         bpName,
		Tracepoint:   true,
	})
	if err != nil {
		return nvim.EchohlErr(v, "Delve", err)
	}
	delve.breakpoints[newbp.ID] = newbp
	if delve.bpSign[newbp.File] == nil {
		delve.bpSign[newbp.File], err = nvim.NewSign(v, "delve_bp", "B>", "Type", "")
		if err != nil {
			return nvim.EchohlErr(v, "Delve", err)
		}
	}

	// Breakpoint 1 at 0x2053 for main.main() /Users/zchee/go/src/github.com/zchee/go-sandbox/astdump/astdump.go:19 (1)
	delvePrintDebug("bp", newbp)
	delvePrintDebug("delve.breakponits", delve.breakpoints)

	sbp := fmt.Sprintf("Breakpoint %d\n\tPC=%#x func=%s() File=%s:%d (%d)",
		newbp.ID,
		newbp.Addr,
		newbp.FunctionName,
		newbp.File,
		newbp.Line,
		newbp.ID)
	bufbp := bytes.NewBufferString(sbp)
	if breaks.linecount, err = printBuffer(v, breaks.buffer, true, bytes.Split(bufbp.Bytes(), []byte{'\n'})); err != nil {
		return nvim.EchohlErr(v, "Delve", err)
	}
	if err := v.SetWindowCursor(breaks.window, [2]int{breaks.linecount, 0}); err != nil {
		return nvim.EchohlErr(v, "Delve", err)
	}

	return nil
}

func delveFunctionList(v *vim.Vim) ([]string, error) {
	funcs, err := delve.client.ListFunctions("main")
	if err != nil {
		return []string{}, nil
	}

	return funcs, nil
}

// parseThread parses the delve Thread information and print the each result
// to the corresponding buffer.
//
// delve original stdout output sample:
//  // continue
//  > main.main() /Users/zchee/go/src/github.com/zchee/golist/golist.go:29 (hits goroutine(1):1 total:1) (PC: 0x20eb)
//  // next
//  > runtime.main() /usr/local/go/src/runtime/proc.go:182 (PC: 0x26e2a)
func parseThread(v *vim.Vim, thread *delveapi.Thread) error {
	if thread != nil {
		funcName := fmt.Sprintf("%s() ", thread.Function.Name)
		file := fmt.Sprintf("%s", thread.File)
		line := fmt.Sprintf(":%d ", thread.Line)
		goroutine := fmt.Sprintf("goroutine(%d) ", thread.GoroutineID)
		pc := fmt.Sprintf("(PC: %#x)", thread.PC)

		var err error
		logs.linecount, err = printBuffer(v, logs.buffer, true, bytes.Split([]byte("(dlv) > "+funcName+file+line+goroutine+pc), []byte{'\n'}))
		if err != nil {
			return err
		}
		if err := v.SetWindowCursor(logs.window, [2]int{logs.linecount, 0}); err != nil {
			return err
		}

		if src.name != thread.File {
			src.name = thread.File
			v.SetBufferName(src.buffer, src.name)

			byt, err := ioutil.ReadFile(thread.File)
			if err != nil {
				return err
			}
			if _, err := printBuffer(v, src.buffer, false, bytes.Split(byt, []byte{'\n'})); err != nil {
				return err
			}

			for _, bp := range delve.breakpoints {
				if bp.File == thread.File {
					delve.bpSign[bp.File].Place(v, bp.ID, bp.Line, src.bufnr, false)
				} else {
					delve.bpSign[bp.File].Unplace(v, bp.ID, src.bufnr)
				}
			}
		}

		delve.pcSign.Place(v, thread.ID, thread.Line, src.bufnr, true)

		if err := v.SetWindowCursor(src.window, [2]int{thread.Line, 0}); err != nil {
			return err
		}

		if stdout.Len() != 0 {
			locals.linecount, err = printBuffer(v, locals.buffer, true, bytes.Split(stdout.Bytes(), []byte{'\n'}))
			if err != nil {
				return err
			}
			if err := v.SetWindowCursor(locals.window, [2]int{locals.linecount, 0}); err != nil {
				return err
			}
			defer stdout.Reset()
		}
	}
	return nil
}

// delveContinue sends the 'continue' signals to the delve headless server over the client use json-rpc2 protocol.
func delveContinue(v *vim.Vim) error {
	stateCh := delve.client.Continue()
	state := <-stateCh

	delvePrintDebug("state", state)
	if state == nil || state.Exited {
		return nvim.Echomsg(v, fmt.Sprintf("Process %d has exited with status %d", delve.procPid, state.ExitStatus))
	}

	if err := parseThread(v, state.CurrentThread); err != nil {
		return err
	}

	breakpoint, err := delve.client.ListBreakpoints()
	if err != nil {
		return err
	}
	sort.Sort(ByID(breakpoint))
	delvePrintDebug("breakpoint", breakpoint)

	var bplines []byte
	for _, bp := range breakpoint {
		if delve.breakpoints[bp.ID].TotalHitCount != bp.TotalHitCount {
			delve.breakpoints[bp.ID].TotalHitCount = bp.TotalHitCount
			delve.breakpoints[bp.ID].HitCount = bp.HitCount
		} else {
			bp = delve.breakpoints[bp.ID]
		}
		sbp := fmt.Sprintf("Breakpoint %d\n\tPC=%#x func=%s() File=%s:%d (%d)\n",
			bp.ID,
			bp.Addr,
			bp.FunctionName,
			bp.File,
			bp.Line,
			bp.ID)
		bufbp := bytes.NewBufferString(sbp)
		bplines = append(bplines, bufbp.Bytes()...)
	}

	if breaks.linecount, err = printBuffer(v, breaks.buffer, false, bytes.Split(bplines, []byte{'\n'})); err != nil {
		return err
	}
	if err := v.SetWindowCursor(breaks.window, [2]int{breaks.linecount, 0}); err != nil {
		return err
	}

	return nil
}

// delveNext sends the 'next' signals to the delve headless server over the client use json-rpc2 protocol.
func delveNext(v *vim.Vim) error {
	state, err := delve.client.Next()
	if err != nil {
		return err
	}

	// delvePrintDebug("state", state)
	if state == nil || state.Exited {
		return nvim.Echomsg(v, fmt.Sprintf("Process %d has exited with status %d", delve.procPid, state.ExitStatus))
	}

	breakpoint, err := delve.client.ListBreakpoints()
	if err != nil {
		return err
	}
	sort.Sort(ByID(breakpoint))
	delvePrintDebug("breakpoint", breakpoint)

	var bplines []byte
	for _, bp := range breakpoint {
		if delve.breakpoints[bp.ID].TotalHitCount != bp.TotalHitCount {
			delve.breakpoints[bp.ID].TotalHitCount = bp.TotalHitCount
			delve.breakpoints[bp.ID].HitCount = bp.HitCount
		} else {
			bp = delve.breakpoints[bp.ID]
		}
		sbp := fmt.Sprintf("Breakpoint %d\n\tPC=%#x func=%s() File=%s:%d (%d)\n",
			bp.ID,
			bp.Addr,
			bp.FunctionName,
			bp.File,
			bp.Line,
			bp.ID)
		bufbp := bytes.NewBufferString(sbp)
		bplines = append(bplines, bufbp.Bytes()...)
	}

	if breaks.linecount, err = printBuffer(v, breaks.buffer, false, bytes.Split(bplines, []byte{'\n'})); err != nil {
		return err
	}
	if err := v.SetWindowCursor(breaks.window, [2]int{breaks.linecount, 0}); err != nil {
		return err
	}

	if err := parseThread(v, state.CurrentThread); err != nil {
		return err
	}
	return nil
}

func printBuffer(v *vim.Vim, b vim.Buffer, append bool, data [][]byte) (int, error) {
	var start int

	// Gets the buffer line count if append is true.
	if append {
		var err error
		start, err = v.BufferLineCount(b)
		if err != nil {
			return 0, err
		}
	}

	// Chceck the target buffer whether empty if line count is 1.
	if start == 1 {
		buf, err := v.BufferLines(b, 0, -1, true)
		if err != nil {
			return 0, err
		}
		// buf[0] is target buffer's first line []byte slice.
		if len(buf[0]) == 0 {
			start = 0
		}
	}

	v.SetBufferOption(b, "modifiable", true)
	defer v.SetBufferOption(b, "modifiable", false)

	return start + len(data), v.SetBufferLines(b, start, -1, true, data)
}

func delveDisassemble(v *vim.Vim) error {
	// delve.c.DisassemblePC()
	return nil
}

func delveRestart(v *vim.Vim) error {
	err := delve.client.Restart()
	if err != nil {
		return err
	}
	return nil
}

func delveDetach(v *vim.Vim) error {
	defer delveKill()
	if delve.procPid == 0 {
		return nil
	}

	if delve.buffers != nil {
		p.SetCurrentTabpage(baseTabpage)
		if err := p.Wait(); err != nil {
			return err
		}

		for _, buf := range delve.buffers {
			v.Command(fmt.Sprintf("bdelete %d", buf.bufnr))
		}
	}
	err := delve.client.Detach(true)
	if err != nil {
		return err
	}
	log.Println("Detached delve client")

	return nil
}

func delveKill() error {
	if server != nil {
		err := server.Process.Kill()
		if err != nil {
			return err
		}
		log.Println("Killed delve server")
	}

	return nil
}

func delvePrintDebug(prefix string, data interface{}) error {
	d, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		log.Println(data)
	}
	log.Println(prefix, "\n", string(d))

	return nil
}