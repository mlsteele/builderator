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
)

type Config struct {
	// Absolute path to the config file. Filled by the reader
	ConfigPath string
	// Paths are absoute-ified by the reader.
	WatchDir    string
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

func RerootPath(in string, relto string) string {
	if !path.IsAbs(in) {
		in = path.Join(relto, in)
	}
	return path.Clean(in)
}

func main() {
	// This method leaks goroutines.

	cpath, err := FindConfig()
	if err != nil {
		die2("Could not find config file", err)
	}

	c, err := ReadConfig(cpath)
	if err != nil {
		die2("Could not read config file", err)
	}

	watchCh := make(chan bool)
	watch(watchCh, c.WatchDir)

	writeStatus(c.StatusFile, "BUILDING")
	buildResultCh, abortCh := build(c.BuildCmdDir, c.BuildFile)
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
			buildResultCh, abortCh = build(c.BuildCmdDir, c.BuildFile)
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

func build(buildDir string, buildFile string) (<-chan BuildResult, chan<- bool) {
	resultCh := make(chan BuildResult)
	abortCh := make(chan bool)

	err := justasec(buildFile)
	if err != nil {
		log.Print(err)
	}

	cmd := exec.Command("go", "install")
	cmd.Dir = buildDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cmd.Start()

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

func die2(reason string, err error) {
	fmt.Fprintf(os.Stderr, "%v: %v\n", reason, err)
	os.Exit(1)
}
