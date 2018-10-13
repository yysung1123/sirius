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
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/getamis/sirius/database/mysql"
	"github.com/getamis/sirius/log"
)

type MySQLContainer struct {
	dockerContainer *Container
	MySQLOptions    MySQLOptions
	Started         bool
	URL             string
}

func (container *MySQLContainer) Start() error {
	container.Started = true
	err := container.dockerContainer.Start()
	if err != nil {
		return err
	}

	if err := updateMySQLContainerHost(container.dockerContainer, &container.MySQLOptions); err != nil {
		return err
	}
	connectionString, _ := ToMySQLConnectionString(container.MySQLOptions)
	container.URL = connectionString
	return nil
}

func (container *MySQLContainer) Suspend() error {
	return container.dockerContainer.Suspend()
}

func (container *MySQLContainer) Stop() error {
	container.Started = false
	return container.dockerContainer.Stop()
}

func (container *MySQLContainer) Teardown() error {
	if container.dockerContainer != nil && container.Started {
		container.Started = false
		return container.dockerContainer.Stop()
	}

	db, err := sql.Open("mysql", container.URL)
	if err != nil {
		return err
	}
	defer db.Close()

	sql := fmt.Sprintf("DROP DATABASE IF EXISTS %s", container.MySQLOptions.Database)
	if _, err = db.Exec(sql); err != nil {
		return err
	}

	return nil
}

// MigrationOptions for mysql migration container
type MigrationOptions struct {
	ImageRepository string
	ImageTag        string

	// this command will override the default command.
	// "bundle" "exec" "rake" "db:migrate"
	Command []string
}

// RunMigrationContainer creates the migration container and connects to the
// mysql database to run the migration scripts.
func RunMigrationContainer(mysql *MySQLContainer, options MigrationOptions) error {
	// the default command
	command := []string{"bundle", "exec", "rake", "db:migrate"}
	if len(options.Command) > 0 {
		command = options.Command
	}

	if len(options.ImageTag) == 0 {
		options.ImageTag = "latest"
	}

	inspectedContainer, err := mysql.dockerContainer.dockerClient.InspectContainer(mysql.dockerContainer.container.ID)
	if err != nil {
		return err
	}

	// Override the mysql host because the migration needs to connect to the
	// mysql server via the docker bridge network directly.
	host := inspectedContainer.NetworkSettings.IPAddress
	port := "3306"
	container := NewDockerContainer(
		ImageRepository(options.ImageRepository),
		ImageTag(options.ImageTag),
		DockerEnv(
			[]string{
				"RAILS_ENV=customized",
				fmt.Sprintf("HOST=%s", host),
				fmt.Sprintf("PORT=%s", port),
				fmt.Sprintf("DATABASE=%s", mysql.MySQLOptions.Database),
				fmt.Sprintf("USERNAME=%s", mysql.MySQLOptions.Username),
				fmt.Sprintf("PASSWORD=%s", mysql.MySQLOptions.Password),
			},
		),
		RunOptions(command),
	)

	if err := container.Start(); err != nil {
		log.Error("Failed to start container", "err", err)
		return err
	}

	if err := container.Wait(); err != nil {
		log.Error("Failed to wait container", "err", err)
		return err
	}

	return container.Stop()
}

type MySQLOptions struct {
	// The following options are used in the connection string and the mysql server container itself.
	Username string
	Password string
	Port     string
	Database string

	// The host address that will be used to build the connection string
	Host string
}

var DefaultMySQLOptions = MySQLOptions{
	Username: "root",
	Password: "my-secret-pw",

	// port 3307 is used to be published on the host.
	// the port number will be changed to 3306 when we connect to the mysql container from
	// another container.
	Port: "3307",

	// The db we want to run the test
	Database: "db0",

	// the mysql host to be connected from the client
	// if we're running test on the host, we might need to connect to the mysql
	// server via 127.0.0.1:3307. however if we want to run the test inside the container,
	// we need to inspect the IP of the container
	Host: "127.0.0.1",
}

func IsInsideContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/bin/running-in-container"); err == nil {
		return true
	}
	return false
}

func NewMySQLHealthChecker(options MySQLOptions) ContainerCallback {
	return func(c *Container) error {
		// We use this connection string to verify the mysql container is ready.
		if err := updateMySQLContainerHost(c, &options); err != nil {
			return err
		}
		connectionString, err := ToMySQLConnectionString(options)
		if err != nil {
			return err
		}

		return retry(10, 3*time.Second, func() error {
			db, err := sql.Open("mysql", connectionString)
			if err != nil {
				return err
			}
			defer db.Close()
			_, err = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", options.Database))
			return err
		})
	}
}

// updateMySQLContainerHost updates the mysql host field according to the current environment
//
// If we're inside the container, we need to override the hostname
// defined in the option.
// If not, we should use the default value 127.0.0.1 because we will need to connect to the host port.
// please note that the TEST_MYSQL_HOST can be overridden.
func updateMySQLContainerHost(c *Container, options *MySQLOptions) error {
	if IsInsideContainer() {
		inspectedContainer, err := c.dockerClient.InspectContainer(c.container.ID)
		if err != nil {
			return err
		}
		options.Host = inspectedContainer.NetworkSettings.IPAddress
	}
	return nil
}

// Convert mysql options to mysql string
func ToMySQLConnectionString(options MySQLOptions) (string, error) {
	// We use this connection string to verify the mysql container is ready.
	return mysql.ToConnectionString(
		mysql.Connector(mysql.DefaultProtocol, options.Host, options.Port),
		mysql.Database(options.Database),
		mysql.UserInfo(options.Username, options.Password),
	)
}

func LoadMySQLOptions() MySQLOptions {
	options := DefaultMySQLOptions

	// mysql container exposes port at 3306, if we're inside a container, we
	// need to use 3306 to connect to the mysql server.
	if IsInsideContainer() {
		options.Port = "3306"
	}

	if host, ok := os.LookupEnv("TEST_MYSQL_HOST"); ok {
		options.Host = host
	}
	if val, ok := os.LookupEnv("TEST_MYSQL_PORT"); ok {
		options.Port = val
	}

	if val, ok := os.LookupEnv("TEST_MYSQL_DATABASE"); ok {
		options.Database = val
	}

	if val, ok := os.LookupEnv("TEST_MYSQL_USERNAME"); ok {
		options.Username = val
	}

	if val, ok := os.LookupEnv("TEST_MYSQL_PASSWORD"); ok {
		options.Password = val
	}
	return options
}

func createMySQLDatabase(options MySQLOptions) error {
	// We must pass mysql.Database to the connection string function, if we
	// don't, the connection string will use "db" as the default database.
	// see https://maicoin.slack.com/archives/G0PKWFTNY/p1539335776000100 for more details.
	connectionString, err := mysql.ToConnectionString(
		mysql.Connector(mysql.DefaultProtocol, options.Host, options.Port),
		mysql.Database(""),
		mysql.UserInfo(options.Username, options.Password),
	)
	if err != nil {
		return err
	}

	db, err := sql.Open("mysql", connectionString)
	if err != nil {
		return err
	}
	defer db.Close()

	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", options.Database)
	_, err = db.Exec(sql)
	return err
}

// setup the mysql connection
// if TEST_MYSQL_HOST is defined, then we will use the connection directly.
// if not, a mysql container will be started
func SetupMySQL() (*MySQLContainer, error) {
	options := LoadMySQLOptions()
	if _, ok := os.LookupEnv("TEST_MYSQL_HOST"); ok {

		connectionString, err := mysql.ToConnectionString(
			mysql.Connector(mysql.DefaultProtocol, options.Host, options.Port),
			mysql.Database(options.Database),
			mysql.UserInfo(options.Username, options.Password),
		)
		if err != nil {
			return nil, fmt.Errorf("Failed to create mysql connection string: %v", err)
		}

		if err := createMySQLDatabase(options); err != nil {
			return nil, fmt.Errorf("Failed to create mysql database: %v", err)
		}

		return &MySQLContainer{
			MySQLOptions: options,
			URL:          connectionString,
		}, nil
	}

	container, err := NewMySQLContainer(options)
	if err != nil {
		return nil, err
	}

	if err := container.Start(); err != nil {
		return container, err
	}

	return container, nil
}

func NewMySQLContainer(options MySQLOptions, containerOptions ...Option) (*MySQLContainer, error) {
	// Once the mysql container is ready, we will create the database if it does not exist.
	checker := NewMySQLHealthChecker(options)

	// In order to let the tests connect to the mysql server, we need to
	// publish the container port 3306 to the host port 3307 only when we're on the host
	if IsInsideContainer() {
		containerOptions = append(containerOptions, ExposePorts("3306"))
	} else {
		// mysql container port always expose the server port on 3306
		containerOptions = append(containerOptions, ExposePorts("3306"))
		containerOptions = append(containerOptions, HostPortBindings(PortBinding{"3306/tcp", options.Port}))
	}

	// Create the container, please note that the container is not started yet.
	container := &MySQLContainer{
		MySQLOptions: options,
		dockerContainer: NewDockerContainer(
			// this is to keep some flexibility for passing extra container options..
			// however if we literally use "..." in the method call, an error
			// "too many arguments" will raise.
			append([]Option{
				ImageRepository("mysql"),
				ImageTag("5.7"),
				DockerEnv(
					[]string{
						fmt.Sprintf("MYSQL_ROOT_PASSWORD=%s", options.Password),
						fmt.Sprintf("MYSQL_DATABASE=%s", options.Database),
					},
				),
				HealthChecker(checker),
			}, containerOptions...)...,
		),
	}

	// please note that: in order to get the correct container address, the
	// connection string will be updated when the container is started.
	connectionString, _ := ToMySQLConnectionString(options)
	container.URL = connectionString
	return container, nil
}
