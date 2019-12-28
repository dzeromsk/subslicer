# subslicer (local-lambda-server) - lambda runtime sandbox
[![GoDoc](https://godoc.org/github.com/dzeromsk/subslicer?status.svg)](https://godoc.org/github.com/dzeromsk/subslicer)

## Overview

`local-lambda-server` is a http server that takes local directory and serves it as a lambda handler. `subslicer` is a golang library to interact with native lambda runtime libraries. Both are written in pure go, without using [docker-lambda](https://github.com/lambci/docker-lambda) or [localstack](https://github.com/localstack/localstack).

## Installation

```bash
$ go get -u github.com/dzeromsk/subslicer/...
```

This will make the `local-lambda-server` tool available in `${GOPATH}/bin`, which by default means `~/go/bin`.

`local-lambda-server` defines `go1.x`, `python2.7` and `python3.7` runtime and expects AWS Lambda chroot to be present:

```bash
mkdir -p $HOME/chroot/go1.x
curl https://lambci.s3.amazonaws.com/fs/base.tgz | sudo tar zxv -C $HOME/chroot/go1.x/
curl https://lambci.s3.amazonaws.com/fs/go1.x.tgz | sudo tar zxv -C $HOME/chroot/go1.x/
```

```bash
mkdir -p $HOME/chroot/python2.7
curl https://lambci.s3.amazonaws.com/fs/base.tgz | sudo tar zxv -C $HOME/chroot/python2.7/
curl https://lambci.s3.amazonaws.com/fs/python2.7.tgz | sudo tar zxv -C $HOME/chroot/python2.7/
```

```bash
mkdir -p $HOME/chroot/python3.7
curl https://lambci.s3.amazonaws.com/fs/base.tgz | sudo tar zxv -C $HOME/chroot/python3.7/
curl https://lambci.s3.amazonaws.com/fs/python3.7.tgz | sudo tar zxv -C $HOME/chroot/python3.7/
```

## Usage of the binary (local-lambda-server)

`local-lambda-server` by default starts http server from current working directory merged with aws ami chroot, initializes native lambda runtime and invokes lambda handler in response to http requests.

```
Usage of local-lambda-server:
  -console string
        Console socket address (default "/tmp/console.sock")
  -debug
        Run with debug flag enabled
  -group string
        Lambda group (default "nogroup")
  -h string
        Lambda runtime handler (default "handler.my_handler")
  -http string
        HTTP address (default "127.0.0.1:9090")
  -logs string
        Logs socket address (default "/tmp/logs.sock")
  -prefix string
        Chroot dir prefix (default $HOME)
  -r string
        Lambda runtime name (default "python2.7")
  -task string
        Lambda task directory (default $CWD)
  -user string
        Lambda user (default "nobody")
  -workers int
        Max workers (default 1)
```

Start server and set runtime to `python2.7`, and handler to `handler.my_handler`:
```bash
sudo local-lambda-server -r python2.7 -h handler.my_handler 
```

***root privileges are required because subslicer uses linux cgroup and seccomp filters***

Invoke lambda handler:
```bash
curl http://127.0.0.1:9090/invoke
```

## Features

 - You edit files in the task dir and server auto reloads handler.
 - Simple server for development.
 - No config files.
 - Does not require docker or anything.
 - It's reasonably fast.
 - Full abi compatibility with AWS Lambda.
 - Tres to reproduce Lambda sandbox syscall filter.
 - Freezes running handlers just like real lambda server does.

## Downsides

 - Supports only single tenant/function.
 - Less features than `localstack`.
 - Requires root privileges because of old cgroup api

## Philosophy

Sometimes you just want to serve lambda handler from local directory similar to how `python -m SimpleHTTPServer` works with static files. `localstack` and `docker-lambda` can do that but it may be slow and clunky. They also replace native lambda runtime with fake/mock interface, ignore lambda syscall filter and cgroup freezer. And sometimes you need all that to iterate quickly when developing lambda with low level functionality or binary libraries.