// Copyright 2014 Zenoss, Inc.
// All rights reserved.
//

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/codegangsta/cli"
	"github.com/control-center/serviced/utils"
	"github.com/garyburd/redigo/redis"
	"github.com/zenoss/glog"
	"net"
)

type ShellService struct {
	redisAddress   string
	name           string
	connectTimeout time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration
}

type Command struct {
	Command     string
	ReturnQueue string
	Time        time.Time
}

type CommandOutput struct {
	Time     time.Time
	Stdout   string
	Stderr   string
	Exited   bool
	ExitCode int
}

type writerToChan struct {
	output chan []byte
}

func newWriterToChan() *writerToChan {
	return &writerToChan{
		output: make(chan []byte, 100),
	}
}

func (w *writerToChan) Write(b []byte) (n int, err error) {
	glog.V(3).Infof("write called: %s", b)
	d := make([]byte, len(b))
	copy(d, b)
	w.output <- d
	return len(b), nil
}

func (w *writerToChan) Close() {
	close(w.output)
}

// createCommand creates a Command object given the bash command c
func (s *ShellService) createCommand(c string) Command {

	uuid, err := utils.NewUUID()
	if err != nil {
		panic(err)
	}

	return Command{
		Command:     c,
		ReturnQueue: "zminion-return-" + uuid,
		Time:        time.Now(),
	}
}

func newShellService(c *cli.Context) *ShellService {
	return &ShellService{
		redisAddress:   c.GlobalString("redis-address"),
		name:           "minion-send-" + c.GlobalString("minion-name"),
		connectTimeout: time.Duration(c.Int("connect-timeout")) * time.Second,
		readTimeout:    time.Duration(c.Int("read-timeout")) * time.Second,
		writeTimeout:   time.Duration(c.Int("write-timeout")) * time.Second,
	}
}

// getOutput retrives the output from the oCommand and
func (s *ShellService) getOutput(outputChan chan CommandOutput, oCommand Command, timeout uint) {
	defer close(outputChan)
	conn, err := s.getConnection()
	if err != nil {
		glog.Errorf("unable to get a connection for %+v: %s", s, err)
		return
	}
	defer conn.Close()
	glog.Infof("waiting for response for %s", oCommand.ReturnQueue)
	for {
		// BLPOP always return 2 items, list name + item
		reply, err := conn.Do("BLPOP", oCommand.ReturnQueue, timeout)
		if reply == nil && err == nil {
			glog.Error("Command timed out.")
			break
		}
		replies, err := redis.Strings(reply, err)
		if err != nil {
			glog.Errorf("unexpected error from BLPOP: %s", err)
			break
		}
		if len(replies) != 2 {
			glog.Info("unexpected return from BLPOP")
			break
		}
		var output CommandOutput
		if err := json.Unmarshal([]byte(replies[1]), &output); err != nil {
			glog.Errorf("Could not unmarshal response: %s", err)
			break
		}
		outputChan <- output
	}
}

// sendCommand will send the command to the redis queue and return a channel to get the result
func (s *ShellService) sendCommand(conn redis.Conn, c Command, timeout uint) (chan CommandOutput, error) {

	b, err := json.Marshal(c)
	if err != nil {
		glog.Errorf("unable to marshal cmd '%+v' err: %s", c, err)
		return nil, err
	}
	conn.Send("RPUSH", s.name, string(b))
	conn.Send("EXPIRE", s.name, timeout)
	if err := conn.Flush(); err != nil {
		glog.Errorf("unable to flush connection '%+v' err: %s", conn, err)
		return nil, err
	}
	output := make(chan CommandOutput, 10)
	go s.getOutput(output, c, timeout)
	return output, nil
}

// getConnection returns a connection to redis
func (s *ShellService) getConnection() (redis.Conn, error) {
	glog.V(1).Infof("getConnection: connectTimeout=%2.1f readTimeout=%2.1f writeTimeout=%2.1f ",
		s.connectTimeout.Seconds(), s.readTimeout.Seconds(), s.writeTimeout.Seconds())
	return redis.DialTimeout("tcp", s.redisAddress, s.connectTimeout, s.readTimeout, s.writeTimeout)
}

// Run sends a command to the redis queue then listens for the execution output
func (s *ShellService) Run(cmd string, printQueueName bool, timeout uint) error {
	conn, err := s.getConnection()
	if err != nil {
		glog.Errorf("unable to getConnection to %+v err: %s", s, err)
		return err
	}
	defer conn.Close()

	// send command to broker
	command := s.createCommand(cmd)
	outputChan, err := s.sendCommand(conn, command, timeout)
	if err != nil {
		return err
	}

	if printQueueName {
		fmt.Println(command.ReturnQueue)
		return nil
	}
	for {
		output, ok := <-outputChan
		if !ok {
			return errors.New("Output channel closed unexpectedly.")
		}
		fmt.Fprintf(os.Stdout, output.Stdout)
		fmt.Fprintf(os.Stderr, output.Stderr)
		if output.Exited {
			os.Exit(output.ExitCode)
		}
	}
}

// shellExecutorProcess executes the given command and sends output to redis
func (s *ShellService) shellExecutorProcess(msg Command, closing chan bool, returnErr chan error, maxSeconds uint) {
	glog.V(1).Infof("shellExecutorProcess getConnection for cmd=%q", msg)
	c, err := s.getConnection()
	if err != nil {
		glog.Errorf("unable to getConnection to %+v err: %s", s, err)
		returnErr <- err
		return
	}
	defer c.Close()

	cmd := exec.Command("/bin/sh", "-c", msg.Command)

	// use special buffers that send writes to a chan
	stdoutChan := newWriterToChan()
	stderrChan := newWriterToChan()
	cmd.Stdout = stdoutChan
	cmd.Stderr = stderrChan

	// setup notification that subprocess exited
	done := make(chan error)
	go func() {
		done <- cmd.Run()
	}()

	flushInterval := time.Tick(time.Second)
	stdoutBuffer := make([]byte, 0)
	stderrBuffer := make([]byte, 0)
	overtime := time.After(time.Second * time.Duration(maxSeconds))

	flush := func(done bool, exitCode int) {
		// don't bother sending output if there is none
		if len(stdoutBuffer) == 0 && len(stderrBuffer) == 0 && !done {
			return
		}
		cMsg := CommandOutput{
			Stdout:   string(stdoutBuffer),
			Stderr:   string(stderrBuffer),
			Time:     time.Now(),
			Exited:   done,
			ExitCode: exitCode,
		}
		msgStr, _ := json.Marshal(cMsg)
		glog.V(2).Infof("sending to %s: %s", msg.ReturnQueue, msgStr)
		c.Send("RPUSH", msg.ReturnQueue, string(msgStr))
		// reader should pick this up in less than maxSeconds seconds
		c.Send("EXPIRE", msg.ReturnQueue, maxSeconds)
		c.Flush()
	}
	for {
		select {

		// handle subprocess exiting
		case err := <-done:
			exitCode, _ := utils.GetExitStatus(err)
			flush(true, exitCode)
			returnErr <- err
			break

		// handle subprocess stdout
		case buffer, ok := <-stdoutChan.output:
			if !ok {
				// channel is closed, stop listenting
				stdoutChan = nil
				continue
			}
			stdoutBuffer = append(stdoutBuffer, buffer...)

		// handle subprocess stderr
		case buffer, ok := <-stderrChan.output:
			if !ok {
				// channel is closed, stop listenting
				stderrChan = nil
				continue
			}
			stderrBuffer = append(stderrBuffer, buffer...)

		// flush to redis periodically
		case <-flushInterval:
			flush(false, 0)
			stdoutBuffer = make([]byte, 0)
			stderrBuffer = make([]byte, 0)

		// subprocess has run too long
		case <-overtime:
			glog.Warning("Killing long execution: %s", cmd)
			cmd.Process.Kill()

		// we got close signal, kill subprocess
		case <-closing:
			cmd.Process.Kill()
			break
		}
	}

}

// shellExecutor manages the execution of a shell commmand
func (s *ShellService) shellExecutor(cmdChan chan Command, maxSeconds uint) {

	var returnErr chan error
	oCmdChan := cmdChan
	closing := make(chan bool)

	for {
		select {

		case <-returnErr:
			returnErr = nil
			cmdChan = oCmdChan

		case cmd, ok := <-cmdChan:
			if !ok {
				close(closing)
				return
			}
			returnErr = make(chan error)
			go s.shellExecutorProcess(cmd, closing, returnErr, maxSeconds)
		}
	}
}

func (s *ShellService) Serve(executors int, maxSeconds uint)  {
	glog.V(1).Infof("Serve: starting %d executors", executors)

	cmdChan := make(chan Command)
	for i := 0; i < executors; i++ {
		go s.shellExecutor(cmdChan, maxSeconds)
	}
	defer close(cmdChan)

	backoff.Reset()
	for {
		glog.Infof("Serve: getConnection")
		c, err := s.getConnection()
		if err == nil {
			func(conn redis.Conn) {
				defer conn.Close()
				for {
					glog.Warningf("calling BLPOP of %s waiting %d seconds", s.name, maxSeconds)
					response, err := redis.Strings(c.Do("BLPOP", s.name, maxSeconds))
					if err == redis.ErrNil {
						glog.Warningf("BLPOP of %s timed out waiting %d seconds", s.name, maxSeconds)
						// The network is working, use the minimum delay before reconnecting
						backoff.Reset()
						return
					} else if err != nil {
						glog.Warningf("Resetting %s connection: %s", s.name, err)
						if err, ok := err.(net.Error); ok && err.Timeout() {
							// a timeout just means there's no work to do, so don't spend
							// a lot of time waiting before trying to get another connection
							backoff.Reset()
						}
						return
					}
					glog.Infof("len(response) = %d", len(response))
					var msg Command
					err = json.Unmarshal([]byte(response[1]), &msg)
					if err != nil {
						glog.Errorf("Could not unmarshal: %s", err)
						return
					}
					backoff.Reset()
					glog.V(2).Infof("Got message: %s, %s", msg.Command, msg.ReturnQueue)
					cmdChan <- msg
				}
			}(c)
		}
		delay := backoff.GetDelay()
		glog.V(1).Infof("sleeping %2.1f second", delay.Seconds())
		time.Sleep(delay)
	}
}

var (
	Version   string
	GoVersion string
	Date      string
	Gitcommit string
	Gitbranch string
	Buildtag  string

	backoff   *Backoff
)

func main() {
	var COMMAND_TIMEOUT uint = 60 * 60	// 1 hour
	var CONNECT_TIMEOUT int = 5		// 5 seconds
	var READ_TIMEOUT int = 60 * 10		// 10 minutes
	var WRITE_TIMEOUT int = 0		// 0 seconds
	var RECONNECT_START_DELAY int = 1	// 1 second
	var RECONNECT_MAX_DELAY int = 90	// 90 seconds

	app := cli.NewApp()
	app.Name = "zminion"
	app.Usage = "a client for distributed bash executions"
	app.Version = Version
	app.Commands = []cli.Command{
		{
			Name:  "serve",
			Usage: "run a slave to execute remote shell commands",
			Flags: []cli.Flag{
				cli.IntFlag{
					Name:  "n",
					Value: 5,
					Usage: "number of concurrent shell executions",
				},
				cli.IntFlag{
					Name:  "max-seconds",
					Value: int(COMMAND_TIMEOUT),
					Usage: "maximum number of seconds subprocess can execute",
				},
				cli.IntFlag{
					Name:  "connect-timeout",
					Value: CONNECT_TIMEOUT,
					Usage: "maximum number of seconds to wait for a connection to Redis",
				},
				cli.IntFlag{
					Name:  "read-timeout",
					Value: READ_TIMEOUT,
					Usage: "maximum number of seconds Redis connection will wait for the input",
				},
				cli.IntFlag{
					Name:  "write-timeout",
					Value: WRITE_TIMEOUT,
					Usage: "maximum number of seconds Redis connection will wait for the output",
				},
				cli.IntFlag{
					Name:  "reconnect-start-delay",
					Value: RECONNECT_START_DELAY,
					Usage: "the initial delay in seconds before attempting to reconnect to Redis",
				},
				cli.IntFlag{
					Name:  "reconnect-max-delay",
					Value: RECONNECT_MAX_DELAY,
					Usage: "the max delay in seconds before attempting to reconnect to Redis",
				},
			},
			Action: func(c *cli.Context) {
				glog.SetVerbosity(c.GlobalInt("verbose"))

				backoff = &Backoff{
					InitialDelay: time.Duration(c.Int("reconnect-start-delay")) * time.Second,
					MaxDelay:     time.Duration(c.Int("reconnect-max-delay")) * time.Second,
					random:       rand.New(rand.NewSource(time.Now().UnixNano())),
				}
				shellService := newShellService(c)
				shellService.Serve(c.Int("n"), uint(c.Int("max-seconds")))
			},
		},
		{
			Name:  "run",
			Usage: "run a command on a remote minion",
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "send-only",
					Usage: "print the name of the return queue and don't print the output",
				},
				cli.IntFlag{
					Name:  "max-seconds",
					Value: int(COMMAND_TIMEOUT),
					Usage: "maximum number of seconds subprocess can execute",
				},
			},
			Action: func(c *cli.Context) {
				glog.SetVerbosity(c.GlobalInt("verbose"))
				shellService := newShellService(c)
				args := c.Args()
				if len(args) < 1 {
					glog.Fatalf("run requires an argument")
				}
				err := shellService.Run(strings.Join(args, " "), c.Bool("send-only"), uint(c.Int("max-seconds")))
				if err != nil {
					glog.Warningf("%v", err)
					os.Exit(100)
				}
			},
		},
		{
			Name:   "version",
			Usage:  "shows version information",
			Action: func(c *cli.Context) {
				shellService := newShellService(c)
				shellService.Version(c)
			},
		},
	}
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "redis-address",
			Value: "localhost:6379",
			Usage: "the redis server address [hostname]:[port]",
		},
		cli.StringFlag{
			Name:  "minion-name",
			Value: "localhost",
			Usage: "the name to use for this minion",
		},
		cli.IntFlag{
			Name:  "verbose",
			Value: 0,
			Usage: "the log level for V logs",
		},
	}
	app.Run(os.Args)
}

func (c *ShellService) Version(ctx *cli.Context) {
	fmt.Printf("Version:    %s\n", Version)
	fmt.Printf("GoVersion:  %s\n", GoVersion)
	fmt.Printf("Gitcommit:  %s\n", Gitcommit)
	fmt.Printf("Gitbranch:  %s\n", Gitbranch)
	fmt.Printf("Date:       %s\n", Date)
	fmt.Printf("Buildtag:   %s\n", Buildtag)
}

// Backoff controls the exponential backoff used when connection attempts to all zookeepers fail
type Backoff struct {
	InitialDelay time.Duration	// the initial delay
	MaxDelay     time.Duration	// The maximum delay
	delay        time.Duration	// the current delay
	random       *rand.Rand
}

// GetDelay returns the amount of delay that should be used for the current connection attempt.
// It will return a randomized value of initialDelay on the first call, and will increase the delay
// randomly on each subsequent call up to maxDelay. The initial delay and each subsequent delay
// are randomized to avoid a scenario where multiple instances on the same host all start trying
// to reconnection. In scenarios like those, we don't want all instances reconnecting in lock-step
// with each other.
func (backoff *Backoff) GetDelay() time.Duration {
	defer func() {
		factor := 2.0
		jitter := 6.0

		backoff.delay = time.Duration(float64(backoff.delay) * factor)
		backoff.delay += time.Duration(backoff.random.Float64() * jitter * float64(time.Second))
		if backoff.delay > backoff.MaxDelay {
			backoff.delay = backoff.MaxDelay
		}
	}()

	if backoff.delay == 0 {
		backoff.Reset()
	}
	glog.V(2).Infof("GetDelay returned backoff interval = %2.1f", backoff.delay.Seconds())
	return backoff.delay
}

// Reset resets the backoff delay to some random value that is btwn 80-120% of the initialDelay.
//     We want to randomize the initial delay so in cases where many instances simultaneously
//     lose all connections, they will not all start trying to reconnect at the same time.
func (backoff *Backoff) Reset() {
	start := backoff.InitialDelay.Seconds()
	minStart := 0.8 * start
	maxStart := 1.2 * start

	// compute a random value between min and max start
	rando := backoff.random.Float64()
	start = minStart + (rando * (maxStart - minStart))
	backoff.delay = time.Duration(start * float64(time.Second))

	// never exceed maxDelay
	if backoff.delay > backoff.MaxDelay {
		backoff.delay = backoff.MaxDelay
	}
}
