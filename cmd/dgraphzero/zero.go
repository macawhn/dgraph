/*
 * Copyright (C) 2017 Dgraph Labs, Inc. and Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package main

import (
	"golang.org/x/net/context"

	"github.com/dgraph-io/dgraph/protos"
)

var emptyMembershipUpdate protos.MembershipUpdate

type Server struct{}

func (s *Server) ShouldServe(
	ctx context.Context, tablet *protos.Tablet) (resp *protos.Payload, err error) {
	resp.Data = []byte("ok")
	return
}

func (s *Server) Update(
	ctx context.Context, membership *protos.Membership) (resp *protos.MembershipUpdate, err error) {
	if ctx.Err() != nil {
		return &emptyMembershipUpdate, ctx.Err()
	}
}
