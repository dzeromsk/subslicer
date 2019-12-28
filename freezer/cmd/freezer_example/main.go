package main

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"freezer"

	"github.com/justincormack/go-memfd"
)

func main() {
	f, err := freezer.NewFreezer("ncat", "-e", "/bin/cat", "-k", "-l", "1235")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	f.Stdout = os.Stdout
	f.Stderr = os.Stderr
	f.Stdin = os.Stdin
	// f.Env = os.Env()

	m1, err := memfd.CreateNameFlags("m1", 0)
	if err != nil {
		log.Fatal(err)
	}
	defer m1.Close()

	m1.WriteString("hello world")

	f.ExtraFiles = []*os.File{m1.File}
	f.Env = []string{
		"M1=" + strconv.Itoa(int(m1.Fd())),
	}

	if err := f.Start(); err != nil {
		log.Fatal(err)
	}

	b := make([]byte, 1)
	for {
		os.Stdin.Read(b)
		fmt.Println("Stop..")
		if err := f.Freeze(); err != nil {
			log.Fatal(err)
		}
		os.Stdin.Read(b)
		fmt.Println("Start..")
		if err := f.Thaw(); err != nil {
			log.Fatal(err)
		}
	}
}
