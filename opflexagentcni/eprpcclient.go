// Copyright 2017 Cisco Systems, Inc.
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

package main

import (
	"net"
	"net/rpc"
	"time"

	cnitypes "github.com/containernetworking/cni/pkg/types"
	md "github.com/noironetworks/aci-containers/cnimetadata"
)

type (
	Client struct {
		connection *rpc.Client
	}
)

func NewClient(dsn string, timeout time.Duration) (*Client, error) {
	connection, err := net.DialTimeout("tcp", dsn, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{connection: rpc.NewClient(connection)}, nil
}

func (c *Client) Register(metadata *md.ContainerMetadata) (*cnitypes.Result, error) {
	var result *cnitypes.Result
	err := c.connection.Call("EpRPC.Register", metadata, &result)
	return result, err
}

func (c *Client) Unregister(id string) (bool, error) {
	var result bool
	err := c.connection.Call("EpRPC.Unregister", id, &result)
	return result, err
}