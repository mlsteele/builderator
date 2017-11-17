package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
)

const (
	CONF_NAME = ".builderator.toml"
)

// See example.toml for config specs.

const STARTER_CONFIG = `# All relative paths are relative to this config file.

# Directory to watch for changes.
WatchDir    = "."

# Command to run when files change. (Can be a script like "./compile.sh")
BuildCmd    = "go install"

# (Optional) Working directory for BuildCmd.
BuildCmdDir = "."

# (Optional) File to write build status and output to.
StatusFile  = "/tmp/buildstatus-builderator"

# (Optional) Target binary to replace with 'justasec' before each build.
BuildFile   = "~/go/bin/builderator"
`

// RawConfig is the config before validation.
// All paths are absolute or relative to the config file.
type RawConfig struct {
	WatchDir    *string
	BuildCmd    *string
	BuildCmdDir *string
	StatusFile  *string
	BuildFile   *string
}

// Validated config. All paths are absolute.
type Config struct {
	// Absolute path to the config file.
	ConfigPath string

	WatchDir    string
	BuildCmd    string
	BuildCmdDir string
	StatusFile  *string
	BuildFile   *string
}

type BuildResult struct {
	Success bool
	Output  string
}

type ConfigNotFoundError struct{}

func NewConfigNotFoundError() error {
	return ConfigNotFoundError{}
}

func (e ConfigNotFoundError) Error() string {
	return fmt.Sprintf("no config file (%v) found", CONF_NAME)
}

// Find the absolute path to a config file.
// Walks up the filesystem looking for a file named CONF_NAME
// `limit` is how many directories up to search. 1 only looks in cwd.
func FindConfig(limit int) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	prev := cwd + "hack"
	for {
		limit--
		if limit < 0 || dir == prev {
			return "", NewConfigNotFoundError()
		}
		// fmt.Printf("@@@ looking in: %v\n", dir)
		cpath := path.Join(dir, CONF_NAME)
		stat, err := os.Stat(cpath)
		if err == nil && !stat.IsDir() {
			return cpath, nil
		}
		prev = dir
		dir = path.Dir(dir)
	}
}

func ReadConfig(cpath string) (Config, error) {
	var rc RawConfig
	var c Config

	if !path.IsAbs(cpath) {
		return c, fmt.Errorf("config path must be absolute: %v", cpath)
	}

	_, err := toml.DecodeFile(cpath, &rc)
	if err != nil {
		return c, err
	}

	c.ConfigPath = path.Clean(cpath)
	confdir := path.Dir(c.ConfigPath)

	if rc.WatchDir == nil {
		return c, fmt.Errorf("missing required config value: WatchDir")
	}
	c.WatchDir, err = RerootPath(*rc.WatchDir, confdir)
	if err != nil {
		return c, err
	}

	if rc.BuildCmd == nil {
		return c, fmt.Errorf("missing required config value: BuildCmd")
	}
	c.BuildCmd = *rc.BuildCmd

	c.BuildCmdDir = confdir
	if rc.BuildCmdDir != nil {
		c.BuildCmdDir, err = RerootPath(*rc.BuildCmdDir, confdir)
		if err != nil {
			return c, err
		}
	}

	if rc.StatusFile != nil {
		s, err := RerootPath(*rc.StatusFile, confdir)
		if err != nil {
			return c, err
		}
		c.StatusFile = &s
	}

	if rc.BuildFile != nil {
		s, err := RerootPath(*rc.BuildFile, confdir)
		if err != nil {
			return c, err
		}
		c.BuildFile = &s
	}

	return c, nil
}

func PrintConfig(c Config) {
	pf := func(a string, b string) {
		fmt.Printf("%s:\n  %s\n", a, b)
	}
	pfo := func(a string, b *string) {
		if b == nil {
			fmt.Printf("%s: None\n", a)
		} else {
			pf(a, *b)
		}
	}
	pf("WatchDir", c.WatchDir)
	pf("BuildCmd", c.BuildCmd)
	pf("BuildCmdDir", c.BuildCmdDir)
	pfo("StatusFile", c.StatusFile)
	pfo("BuildFile", c.BuildFile)
}

// RerootPath takes a path and makes sure it's absolute.
// If it was relative, it is treated as relative to relto.
func RerootPath(p string, relto string) (string, error) {
	var err error
	p, err = Homeopathy(p)
	if err != nil {
		return "", err
	}
	p = os.ExpandEnv(p)
	if !path.IsAbs(p) {
		p = path.Join(relto, p)
	}
	p = path.Clean(p)
	return p, nil
}

// Homeopathy takes a path and expands the ~ part of it if there is one.
// It is not always possible to do this, or so they say.
func Homeopathy(p string) (string, error) {
	homefirst := func(q string) (string, error) {
		usr, err := user.Current()
		if err != nil {
			return "", err
		}
		dir := usr.HomeDir
		if len(dir) == 0 {
			return "", errors.New("no user homedir set")
		}
		return path.Join(dir, q), nil
	}

	switch {
	case len(p) == 1 && p == "~":
		return homefirst("")
	case len(p) >= 2 && p[:2] == "~/":
		return homefirst(p[2:])
	}

	return p, nil
}

func usage() {
	fmt.Printf("Usage: %s\n       %s mon\n", os.Args[0], os.Args[0])
	flag.PrintDefaults()
}

func main() {
	// This method leaks goroutines.

	flag.Usage = usage

	var cpath0 string
	flag.StringVar(&cpath0, "c", "", "Config file path")
	var generateStarter bool
	flag.BoolVar(&generateStarter, "g", false, "Generate: create a .builderator.toml with a default config")
	var dryrun bool
	flag.BoolVar(&dryrun, "n", false, "Dryrun: print parsed config and exit")
	var once bool
	flag.BoolVar(&once, "o", false, "Once: Run the build command once and exit")
	// TODO add flag --quiet silences the output unless there's an error

	flag.Parse()

	mon := false

	switch {
	case flag.NArg() == 0:
	case flag.NArg() == 1 && flag.Arg(0) == "mon":
		mon = true
	default:
		usage()
		die("Incorrect usage")
	}

	if generateStarter {
		err := generate()
		if err != nil {
			die(fmt.Sprintf("Could not generate config: %v\n", err))
		}
		return
	}

	var cpath string
	if len(cpath0) == 0 {
		foundpath, err := FindConfig(64)
		if err != nil {
			die(fmt.Sprintf("Could not find config file: %v\n", err))
		}
		cpath = foundpath
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			die(fmt.Sprintf("Could not get cwd"))
		}
		cpath, err = RerootPath(cpath0, cwd)
		if err != nil {
			die(fmt.Sprintf("Could not find config file: %v\n", err))
		}
	}

	c, err := ReadConfig(cpath)
	if err != nil {
		die2("Could not read config file", err)
	}

	if mon {
		monitor(c)
		return
	}

	switch flag.NArg() {
	case 0:
	case 1:
		if flag.Arg(0) == "mon" {
			die("mon not implemented")
		} else {
			usage()
			die("Incorrect usage")
		}
	}

	if dryrun {
		fmt.Fprintf(os.Stderr, "Config path:\n  %v\n", cpath)
		PrintConfig(c)
		fmt.Fprintf(os.Stderr, "\nDryrun complete\n")
		return
	}

	PrintConfig(c)

	watchCh := make(chan bool)
	watch(watchCh, c.WatchDir)

	if c.StatusFile != nil {
		writeStatus(*c.StatusFile, "BUILDING")
	}
	buildResultCh, abortCh := build(c)
	active := true

	for {
		select {
		case <-watchCh:
			fmt.Printf("<- change\n")
			if active {
				abortCh <- true

				if c.StatusFile != nil {
					writeStatus(*c.StatusFile, "CANCELING")
				}

				// Wait for the abort to effect.
				res := <-buildResultCh
				err := report(c, res)
				if err != nil {
					log.Print(err)
				}
				if once {
					return
				}
			}

			if c.StatusFile != nil {
				writeStatus(*c.StatusFile, "BUILDING")
			}
			buildResultCh, abortCh = build(c)
			active = true
		case res := <-buildResultCh:
			err := report(c, res)
			if err != nil {
				log.Print(err)
			}
			active = false
			if once {
				return
			}
		}
	}
}

func generate() error {
	// Make sure a config doesn't already exist in this directory.
	_, err := FindConfig(1)
	switch err.(type) {
	case ConfigNotFoundError:
		// good
	case nil:
		return fmt.Errorf("Config already exists in this directory")
	default:
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cpath := path.Join(cwd, CONF_NAME)
	return ioutil.WriteFile(cpath, []byte(STARTER_CONFIG), 0644)
}

func report(c Config, res BuildResult) error {
	if res.Success {
		if c.StatusFile != nil {
			writeStatus(*c.StatusFile, fmt.Sprintf("ok\n\n%v", res.Output))
		}
	} else {
		if c.StatusFile != nil {
			writeStatus(*c.StatusFile, fmt.Sprintf("FAILED\n\n%v", res.Output))
		}
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
	if c.BuildFile != nil {
		err := justasec(*c.BuildFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not replace with justasec: %v\n", err)
		}
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
		if exit != nil {
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

func monitor(c Config) {
	if c.StatusFile == nil {
		die("Config.StatusFile required for Monitor mode")
	}

	binary, err := exec.LookPath("watch")
	if err != nil {
		die(fmt.Sprintf("could not find watch program: %s", err))
	}

	// TODO this is broken
	die("not implemented")

	env := os.Environ()
	args := []string{binary, "-n", ".1", *c.StatusFile}
	log.Printf("%+v", args)
	err = syscall.Exec(binary, args, env)
	if err != nil {
		die(fmt.Sprintf("error running watch: %s", err))
	}
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
