// Copyright 2017 AMIS Technologies
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test

import (
	"context"
	"fmt"
	"os"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/getamis/sirius/crypto/rand"
)

type Container struct {
	dockerClient     *docker.Client
	name             string
	imageRespository string
	imageTag         string

	portBindings map[docker.Port][]docker.PortBinding
	exposedPorts map[docker.Port]struct{}

	ports         []string
	runArgs       []string
	envs          []string
	container     *docker.Container
	healthChecker ContainerCallback
	initializer   ContainerCallback
}

type ContainerCallback func(*Container) error

func NewDockerContainer(opts ...Option) *Container {
	c := &Container{
		dockerClient: newDockerClient(),
		healthChecker: func(c *Container) error {
			return nil
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	// Automatically convert the ports to exposed ports and host binding ports
	if c.portBindings == nil {
		c.portBindings = make(map[docker.Port][]docker.PortBinding)
	}
	if c.exposedPorts == nil {
		c.exposedPorts = make(map[docker.Port]struct{})
	}

	if len(c.ports) > 0 {
		c.exposedPorts = make(map[docker.Port]struct{})
		for _, port := range c.ports {
			c.AddHostPortBinding(port, port)
			c.ExposePort(port)
		}
	}

	var err error
	c.container, err = c.dockerClient.CreateContainer(docker.CreateContainerOptions{
		Name: c.name + generateNameSuffix(),
		Config: &docker.Config{
			Image:        c.imageRespository + ":" + c.imageTag,
			Cmd:          c.runArgs,
			ExposedPorts: c.exposedPorts,
			Env:          c.envs,
		},
		HostConfig: &docker.HostConfig{
			PortBindings: c.portBindings,
		},
		Context: context.TODO(),
	})
	if err != nil {
		panic(fmt.Errorf("Failed to create a container %s:%s error:%s", c.imageRespository, c.imageTag, err))
	}

	return c
}

func newDockerClient() *docker.Client {
	var client *docker.Client
	if os.Getenv("DOCKER_MACHINE_NAME") != "" {
		client, _ = docker.NewClientFromEnv()
	} else {
		client, _ = docker.NewClient("unix:///var/run/docker.sock")
	}
	return client
}

func (c *Container) OnReady(initializer ContainerCallback) {
	c.initializer = initializer
}

func (c *Container) Start() error {
	err := c.dockerClient.StartContainer(c.container.ID, nil)
	if err != nil {
		return err
	}

	defer func() {
		if c.initializer != nil {
			err = c.initializer(c)
		}
	}()
	err = c.healthChecker(c)
	return err
}

func (c *Container) ExposePort(port string) {
	c.exposedPorts[docker.Port(port)] = struct{}{}
}

func (c *Container) AddHostPortBinding(containerPort string, hostPort string) {
	c.portBindings[docker.Port(containerPort)] = []docker.PortBinding{
		{
			HostIP:   "0.0.0.0",
			HostPort: hostPort,
		},
	}
}

func (c *Container) Suspend() error {
	return c.dockerClient.StopContainer(c.container.ID, 0)
}

func (c *Container) Wait() error {
	_, err := c.dockerClient.WaitContainer(c.container.ID)
	return err
}

func (c *Container) Stop() error {
	return c.dockerClient.RemoveContainer(docker.RemoveContainerOptions{
		ID:      c.container.ID,
		Force:   true,
		Context: context.TODO(),
	})
}

func generateContainerID() string {
	return rand.New(
		rand.HexEncoder(),
	).KeyEncoded()
}

func generateNameSuffix() string {
	return rand.New(
		rand.UUIDEncoder(),
	).KeyEncoded()
}
