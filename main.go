// Copyright 2014 Zenoss, Inc.
// All rights reserved.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/codegangsta/cli"
	"github.com/control-center/serviced/utils"
	"github.com/garyburd/redigo/redis"
	"github.com/zenoss/glog"
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

// getOutput retrives the output from the oCommand and
func (s *ShellService) getOutput(outputChan chan CommandOutput, oCommand Command) {
	conn, err := s.getConnection()
	if err != nil {
		return
	}
	defer conn.Close()
	glog.Infof("waiting for response for %s", oCommand.ReturnQueue)
	for {
		// BLPOP always return 2 items, list name + item
		replies, err := redis.Strings(conn.Do("BLPOP", oCommand.ReturnQueue, 30))
		if err != nil {
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
func (s *ShellService) sendCommand(conn redis.Conn, c Command) (chan CommandOutput, error) {

	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	conn.Send("RPUSH", s.name, string(b))
	conn.Send("EXPIRE", s.name, 120)
	if err := conn.Flush(); err != nil {
		return nil, err
	}
	output := make(chan CommandOutput, 10)
	go s.getOutput(output, c)
	return output, nil
}

// getConnection returns a connection to redis
func (s *ShellService) getConnection() (redis.Conn, error) {
	return redis.DialTimeout("tcp", s.redisAddress, s.connectTimeout, s.readTimeout, s.writeTimeout)
}

// Run sends a command to the redis queue then listens for the execution output
func (s *ShellService) Run(cmd string, printQueueName bool) error {
	conn, err := s.getConnection()
	if err != nil {
		return err
	}
	defer conn.Close()

	// send command to broker
	command := s.createCommand(cmd)
	output, err := s.sendCommand(conn, command)
	if err != nil {
		return err
	}

	if printQueueName {
		fmt.Println(command.ReturnQueue)
		return nil
	}
	for {
		output, ok := <-output
		if !ok {
			return nil
		}
		fmt.Fprintf(os.Stdout, output.Stdout)
		fmt.Fprintf(os.Stderr, output.Stderr)
		if output.Exited {
			os.Exit(output.ExitCode)
		}
	}

	return nil
}

// shellExecutorProcess executes the given command and sends output to redis
func (s *ShellService) shellExecutorProcess(msg Command, closing chan bool, returnErr chan error, maxSeconds int) {
	c, err := s.getConnection()
	if err != nil {
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
		glog.V(1).Infof("sending to %s: %s", msg.ReturnQueue, msgStr)
		c.Send("RPUSH", msg.ReturnQueue, string(msgStr))
		// reader should pick this up in less than 600 seconds
		c.Send("EXPIRE", msg.ReturnQueue, 600)
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
func (s *ShellService) shellExecutor(cmdChan chan Command, maxSeconds int) {

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

func (s *ShellService) Serve(executors int, maxSeconds int) error {
	glog.V(2).Info("Serve")

	cmdChan := make(chan Command)
	for i := 0; i < executors; i++ {
		go s.shellExecutor(cmdChan, maxSeconds)
	}
	defer close(cmdChan)
	for {

		c, err := redis.DialTimeout("tcp", s.redisAddress, s.connectTimeout, s.readTimeout, s.writeTimeout)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		func(conn redis.Conn) {
			defer conn.Close()
			for {
				response, err := redis.Strings(c.Do("BLPOP", s.name, 10))
				if err != nil {
					return
				}
				glog.Infof("len(response) = %d", len(response))
				var msg Command
				err = json.Unmarshal([]byte(response[1]), &msg)
				if err != nil {
					glog.Errorf("Could not unmarshal: %s", err)
					return
				}
				glog.V(1).Infof("Got message: %s, %s", msg.Command, msg.ReturnQueue)
				cmdChan <- msg
			}
		}(c)
	}
	return nil
}

func main() {
	app := cli.NewApp()
	app.Name = "zminion"
	app.Usage = "a client for distributed bash executions"
	app.Version = "0.1"
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
					Value: 120,
					Usage: "maxinum number of seconds subpocess can execute",
				},
			},
			Action: func(c *cli.Context) {
				shellService := ShellService{
					redisAddress: c.GlobalString("redis-address"),
					name:         "minion-send-" + c.GlobalString("minion-name"),
				}
				shellService.Serve(c.Int("n"), c.Int("max-seconds"))
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
			},
			Action: func(c *cli.Context) {
				shellService := ShellService{
					redisAddress: c.GlobalString("redis-address"),
					name:         "minion-send-" + c.GlobalString("minion-name"),
				}
				args := c.Args()
				if len(args) < 1 {
					glog.Fatalf("run requires an argument")
				}
				shellService.Run(args[0], c.Bool("send-only"))
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
	}
	app.Run(os.Args)
}
