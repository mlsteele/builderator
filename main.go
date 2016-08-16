package main

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/BurntSushi/toml"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"syscall"
	"strings"
	"errors"
	"os/user"
)

type Config struct {
	// Absolute path to the config file. Filled by the reader
	ConfigPath string
	// Paths are absoute-ified by the reader.
	WatchDir    string
	BuildCmd    string
	BuildCmdDir string
	BuildFile   string
	StatusFile  string
}

type BuildResult struct {
	Success bool
	Output  string
}

// Find the absolute path to a config file.
func FindConfig() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return path.Join(cwd, ".builderator.toml"), nil
}

func ReadConfig(cpath string) (Config, error) {
	// TODO require all required fields
	var c Config

	_, err := toml.DecodeFile(cpath, &c)
	if err != nil {
		return c, err
	}

	c.ConfigPath = path.Clean(cpath)
	confdir := path.Dir(c.ConfigPath)
	c.WatchDir = RerootPath(c.WatchDir, confdir)
	c.BuildCmdDir = RerootPath(c.BuildCmdDir, confdir)
	c.BuildFile = RerootPath(c.BuildFile, confdir)
	c.StatusFile = RerootPath(c.StatusFile, confdir)

	return c, nil
}

func RerootPath(p string, relto string) string {
	var err error
	p, err = Homeopathy(p)
	if err != nil {
		die(fmt.Sprintf("Could not understand path %v: %v", p, err))
	}
	p = os.ExpandEnv(p)
	if !path.IsAbs(p) {
		p = path.Join(relto, p)
	}
	p = path.Clean(p)
	return p
}

func Homeopathy(p string) (string, error) {
	if p[:2] == "~/" {
		usr, err := user.Current()
		if err != nil {
			return "", err
		}
		dir := usr.HomeDir
		if len(dir) == 0 {
			return "", errors.New("no user homedir set")
		}
		p = path.Join(dir, p[2:])
	}
	return p, nil
}

func main() {
	// This method leaks goroutines.

	cpath, err := FindConfig()
	if err != nil {
		die(fmt.Sprintf("Could not find config file: %v\n%v\n", cpath, err))
	}

	c, err := ReadConfig(cpath)
	if err != nil {
		die2("Could not read config file", err)
	}

	watchCh := make(chan bool)
	watch(watchCh, c.WatchDir)

	writeStatus(c.StatusFile, "BUILDING")
	buildResultCh, abortCh := build(c)
	active := true

	for {
		select {
		case <-watchCh:
			fmt.Printf("<- change\n")
			if active {
				abortCh <- true

				writeStatus(c.StatusFile, "CANCELING")

				// Wait for the abort to effect.
				res := <-buildResultCh
				err := report(c, res)
				if err != nil {
					log.Print(err)
				}
			}

			writeStatus(c.StatusFile, "BUILDING")
			buildResultCh, abortCh = build(c)
			active = true
		case res := <-buildResultCh:
			err := report(c, res)
			if err != nil {
				log.Print(err)
			}
			active = false
		}
	}
}

func report(c Config, res BuildResult) error {
	if res.Success {
		writeStatus(c.StatusFile, fmt.Sprintf("ok\n\n%v", res.Output))
	} else {
		writeStatus(c.StatusFile, fmt.Sprintf("FAILED\n\n%v", res.Output))
	}
	fmt.Printf("<- build %v\n", res.Success)
	return nil
}

// Kick off a single build run.
// Returns channels to get the result and abort the build.
// A single result is always returned on the resultCh even when aborted.
// (There may be a race where two buildresults are returned in aborted)
func build(c Config) (<-chan BuildResult, chan<- bool) {
	resultCh := make(chan BuildResult)
	abortCh := make(chan bool)

	// Replace the target with justasec.
	err := justasec(c.BuildFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not replace with justasec: %v\n", err)
	}

	// cmd := exec.Command("go", "install")
	cmdparts := strings.Split(c.BuildCmd, " ")
	if len(cmdparts) < 1 {
		die(fmt.Sprintf("Invalid command: %v", cmdparts))
	}
	cmd := exec.Command(cmdparts[0], cmdparts[1:]...)
	cmd.Dir = c.BuildCmdDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmd.Start()

	// Receiver for aborting
	go func() {
		<-abortCh
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err == nil {
			syscall.Kill(-pgid, 15)
		}
		cmd.Wait()
		resultCh <- BuildResult{
			Success: false,
			Output:  "Build canceled",
		}
	}()

	// Receiver for completion
	go func() {
		exit := cmd.Wait()
		if err != nil {
			fmt.Printf("build exit: %v\n", exit)
		}

		res := BuildResult{
			Success: exit == nil,
			Output:  fmt.Sprintf("%v%v", string(stdout.Bytes()), string(stderr.Bytes())),
		}
		resultCh <- res
	}()

	return resultCh, abortCh
}

func watch(ch chan<- bool, watchDir string) {
	cmd := exec.Command("fswatch", watchDir,
		"--event", "Updated",
		"--latency", "0.101",
		"--one-per-batch")
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

func justasec(binpath string) error {
	jaspath := "/Users/miles/go/bin/justasec"
	cmd := exec.Command("cp", jaspath, binpath)
	return cmd.Run()
}

func die(reason string) {
	fmt.Fprintf(os.Stderr, "%v\n", reason)
	os.Exit(1)
}

func die2(reason string, err error) {
	fmt.Fprintf(os.Stderr, "%v: %v\n", reason, err)
	os.Exit(1)
}
