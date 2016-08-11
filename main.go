package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
)

type BuildResult struct {
	Success bool
	Output  string
}

func main() {
	// This method leaks goroutines.

	watchDir := "/Users/miles/go/src/github.com/keybase/client/go/"
	buildDir := "/Users/miles/go/src/github.com/keybase/client/go/keybase/"
	buildFile := "/Users/miles/go/bin/keybase"
	statusFile := "/tmp/buildstatus"

	watchCh := make(chan bool)
	watch(watchCh, watchDir)

	writeStatus(statusFile, "BUILDING")
	buildResultCh, abortCh := build(buildDir, buildFile)
	active := true

	for {
		select {
		case <-watchCh:
			fmt.Printf("<- change\n")
			if active {
				abortCh <- true
			}
			writeStatus(statusFile, "BUILDING")
			buildResultCh, abortCh = build(buildDir, buildFile)
		case res := <-buildResultCh:
			if res.Success {
				writeStatus(statusFile, fmt.Sprintf("ok\n\n%v", res.Output))
			} else {
				writeStatus(statusFile, fmt.Sprintf("FAILED\n\n%v", res.Output))
			}
			fmt.Printf("<- build %v\n", res.Success)
			active = false
		}
	}
}

func build(buildDir string, buildFile string) (<-chan BuildResult, chan<- bool) {
	resultCh := make(chan BuildResult)
	abortCh := make(chan bool)

	cmd := exec.Command("go", "install")
	cmd.Dir = buildDir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmd.Start()

	go func() {
		<-abortCh
		cmd.Process.Signal(os.Kill)
	}()

	go func() {
		exit := cmd.Wait()

		res := BuildResult{
			Success: exit == nil,
			Output:  fmt.Sprintf("%v%v", string(stdout.Bytes()), string(stderr.Bytes())),
		}
		resultCh <- res
	}()

	return resultCh, abortCh
}

func watch(ch chan<- bool, watchDir string) {
	cmd := exec.Command("fswatch", watchDir, "--event", "Updated", "-l", "0.101")
	cmd.Dir = watchDir

	outReader, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	outScanner := bufio.NewScanner(outReader)

	go func() {
		for outScanner.Scan() {
			_ = outScanner.Text()
			ch <- true
		}
	}()

	cmd.Start()

}

func writeStatus(path string, status string) {
	b := []byte(status)
	err := ioutil.WriteFile(path, b, 0644)
	if err != nil {
		fmt.Printf("WARN: could not write to status file\n")
	}
}
