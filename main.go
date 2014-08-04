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
func (s *ShellService) Run(cmd string) error {
	conn, err := s.getConnection()
	if err != nil {
		return err
	}
	defer conn.Close()

	command := s.createCommand(cmd)
	output, err := s.sendCommand(conn, command)
	if err != nil {
		return err
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

	cmd := exec.Command("/bin/sh", "-c", msg.Command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		glog.Errorf("Could not open stdout: %s", err)
		returnErr <- err
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		glog.Errorf("Could not open stderr: %s", err)
		returnErr <- err
		return
	}

	if err := cmd.Start(); err != nil {
		glog.Errorf("Could not start subprocess: %s", err)
		returnErr <- err
		return
	}

	stdoutBuffer := make([]byte, 1024*8)
	stderrBuffer := make([]byte, 1024*8)
	overtime := time.After(time.Duration(maxSeconds) * time.Second)
	for {
		stdoutN, serr := stdout.Read(stdoutBuffer)
		stderrN, eerr := stderr.Read(stderrBuffer)
		if stdoutN > 0 || stderrN > 0 {
			cMsg := CommandOutput{
				Stdout: string(stdoutBuffer[0:stdoutN]),
				Stderr: string(stdoutBuffer[0:stderrN]),
				Time:   time.Now(),
			}
			msgStr, _ := json.Marshal(cMsg)
			glog.V(2).Infof("sending to %s: %s", msg.ReturnQueue, msgStr)
			c.Send("RPUSH", msg.ReturnQueue, string(msgStr))
			c.Send("EXPIRE", msg.ReturnQueue, 600)
			c.Flush()
			continue
		}
		if serr != nil || eerr != nil {
			break
		}
		select {
		case <-overtime:
			glog.Warning("Killing long execution: %s", cmd)
			cmd.Process.Kill()
			returnErr <- nil
			return
		case <-closing:
			cmd.Process.Kill()
			returnErr <- nil
			return
		case <-time.After(time.Second):
		}
	}
	glog.Infof("waiting for process to exit")
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		glog.Errorf("error running command: %s", err)
		exitCode, _ = utils.GetExitStatus(err)
	}
	oMsg := CommandOutput{
		Time:     time.Now(),
		Exited:   true,
		ExitCode: exitCode,
	}
	msgBytes, _ := json.Marshal(oMsg)
	c.Send("RPUSH", msg.ReturnQueue, string(msgBytes))
	c.Flush()
	returnErr <- nil

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
			Action: func(c *cli.Context) {
				shellService := ShellService{
					redisAddress: c.GlobalString("redis-address"),
					name:         "minion-send-" + c.GlobalString("minion-name"),
				}
				args := c.Args()
				if len(args) < 1 {
					glog.Fatalf("run requires an argument")
				}
				shellService.Run(args[0])
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
