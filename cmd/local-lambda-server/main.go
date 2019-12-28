package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dzeromsk/subslicer"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

var (
	consoleAddr  = flag.String("console", "/tmp/console.sock", "Console socket address")
	logsAddr     = flag.String("logs", "/tmp/logs.sock", "Logs socket address")
	httpAddr     = flag.String("http", "127.0.0.1:9090", "HTTP address")
	task         = flag.String("task", taskdir(), "Lambda task directory")
	prefix       = flag.String("prefix", homedir(), "Chroot dir prefix")
	username     = flag.String("user", "nobody", "Lambda user")
	groupname    = flag.String("group", "nogroup", "Lambda group")
	handler      = flag.String("h", "handler.my_handler", "Lambda runtime handler")
	executionEnv = flag.String("r", "python2.7", "Lambda runtime name")
	workers      = flag.Int64("workers", 1, "Max workers")
	debug        = flag.Bool("debug", false, "Run with debug flag enabled")

	xrayAddr = "127.0.0.1:9090"
)

var runtimes = map[string]subslicer.Runtime{
	"python2.7": subslicer.Runtime{
		Name:   "python2.7",
		Cmd:    "/usr/bin/python",
		Args:   []string{"/var/runtime/awslambda/bootstrap.py"},
		Chroot: "$PREFIX/chroot/python2.7",
	},
	"python3.7": subslicer.Runtime{
		Name:   "python3.7",
		Cmd:    "/var/rapid/init",
		Args:   []string{"--bootstrap", "/var/runtime/bootstrap"},
		Chroot: "$PREFIX/chroot/python3.7",
	},
	"go1.x": subslicer.Runtime{
		Name:   "go1.x",
		Cmd:    "/var/runtime/aws-lambda-go",
		Chroot: "$PREFIX/chroot/go1.x",
	},
}

var taskdir = func() string { dir, _ := os.Getwd(); return dir }
var homedir = func() string { dir, _ := os.UserHomeDir(); return dir }

func main() {
	flag.Parse()

	var (
		consoleAddr = &net.UnixAddr{Net: "unix", Name: *consoleAddr}
		logsAddr    = &net.UnixAddr{Net: "unix", Name: *logsAddr}
	)

	r, ok := runtimes[*executionEnv]
	if !ok {
		log.Fatalln("Unknown runtime:", *executionEnv)
	}
	log.Println("Selected runtime:", *executionEnv)

	// Logs
	console, err := subslicer.NewUNIXServer(consoleAddr, func(conn net.Conn) {
		// TODO(dzeromsk): handle protocol messages
		s := bufio.NewScanner(conn)
		for s.Scan() {
			fmt.Println("console:", s.Text())
		}
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer console.Close()

	// Logs
	logs, err := subslicer.NewUNIXServer(logsAddr, func(conn net.Conn) {
		if *debug {
			s := bufio.NewScanner(conn)
			for s.Scan() {
				fmt.Println("logs:", s.Text())
			}
		} else {
			// should we care?
			io.Copy(ioutil.Discard, conn)
		}
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer logs.Close()

	// XRay
	xray, err := subslicer.NewUDPServer(xrayAddr, func(data []byte) {
		fmt.Println("xray", string(data))
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer xray.Close()

	r.ConsoleAddr = consoleAddr
	r.LogsAddr = logsAddr
	r.User = *username
	r.Group = *groupname
	r.Chroot = strings.Replace(r.Chroot, "$PREFIX", *prefix, 1)

	// Bootstrap
	fp := subslicer.FunctionPool{
		New: func() (f *subslicer.Function, err error) {
			log.Println("Starting lambda function:", *handler)
			f, err = subslicer.NewFunction(r, *task, *handler)
			if err != nil {
				return
			}
			f.Stdout = os.Stdout
			f.Stderr = os.Stderr
			return
		},
	}
	defer fp.Purge()

	var sem = semaphore.NewWeighted(*workers)

	http.HandleFunc("/favicon.ico", http.NotFound)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// TODO(dzeromsk): context with timeout
		ctx := context.Background()

		if err := sem.Acquire(ctx, 1); err != nil {
			log.Println(err)
			return
		}
		defer sem.Release(1)

		f, err := fp.Get()
		if err != nil {
			http.Error(w, "function init failed", http.StatusInternalServerError)
			log.Println(err)
			return
		}
		defer fp.Put(f)

		n, err := io.Copy(f, r.Body)
		if err != nil {
			if err != io.EOF {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				log.Println(err)
				return
			}
		}
		if n == 0 {
			f.Write([]byte("{}"))
		}

		if err := f.Thaw(); err != nil {
			http.Error(w, "thaw failed", http.StatusInternalServerError)
			log.Println(err)
			return
		}

		if err := f.Invoke(ctx); err != nil {
			http.Error(w, "invoke failed", http.StatusInternalServerError)
			log.Println(err)
			return
		}

		if err := f.Freeze(); err != nil {
			http.Error(w, "freeze failed", http.StatusInternalServerError)
			log.Println(err)
			return
		}

		if *debug {
			debug := string(f.Debug())
			if len(debug) > 0 && debug != "{}" {
				log.Println(len(debug), debug)
			}
		}

		w.Write(f.Response())
		f.Reset()
	})

	// TODO(dzeromsk): use group with context shared with other servers
	// TODO(dzeromsk): should we add (*function).Wait() to errgroup and
	// catch freezer errors?
	var g errgroup.Group

	g.Go(func() error {
		log.Println("Starting console server:", consoleAddr)
		return console.Serve()
	})

	g.Go(func() error {
		log.Println("Starting log server:", logsAddr)
		return logs.Serve()
	})

	g.Go(func() error {
		log.Println("Starting xray server:", xrayAddr)
		return xray.Serve()
	})

	// invoke server
	g.Go(func() error {
		log.Println("Starting http server:", *httpAddr)
		return http.ListenAndServe(*httpAddr, nil)
	})

	// reload with naive debounce
	purge := make(chan struct{})
	g.Go(func() error {
		needsPurge := 0
		for {
			var timerChan <-chan time.Time
			if needsPurge > 0 {
				timerChan = time.After(200 * time.Millisecond)
			} else {
				timerChan = make(chan time.Time)
			}
			select {
			case <-purge:
				needsPurge++
				continue
			case <-timerChan:
				needsPurge = 0
				log.Println("Reload")
				fp.Purge()
			}
		}
	})

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	// TODO(dzeromsk): add subdirs recursively
	if err := watcher.Add(*task); err != nil {
		log.Fatal(err)
	}

	// fs event watcher
	g.Go(func() error {
		for {
			select {
			case _, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				// TODO(dzeromsk): handle create event and add dirs recursively
				purge <- struct{}{}
			case err, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
				log.Println("error:", err)
				return err
			}
		}
	})

	// TODO(dzeromsk): move to errgroup
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Println("Signal")
		// TODO(dzeromsk): handle errors and cleanup
		fp.Purge()
		console.Close()
		logs.Close()
		xray.Close()
		os.Remove(consoleAddr.Name)
		os.Remove(logsAddr.Name)
		os.Exit(1)
	}()

	if err := g.Wait(); err != nil {
		log.Fatal(err)
	}
}
