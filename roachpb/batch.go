// Copyright 2014 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License. See the AUTHORS file
// for names of contributors.
//
// Author: Tobias Schottdorf

package roachpb

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
)

// IsAdmin returns true iff the BatchRequest contains an admin request.
func (ba *BatchRequest) IsAdmin() bool {
	return ba.flags()&isAdmin != 0
}

// IsWrite returns true iff the BatchRequest contains a write.
func (ba *BatchRequest) IsWrite() bool {
	return (ba.flags() & isWrite) != 0
}

// IsReadOnly returns true if all requests within are read-only.
func (ba *BatchRequest) IsReadOnly() bool {
	flags := ba.flags()
	return (flags&isRead) != 0 && (flags&isWrite) == 0
}

// IsReverse returns true iff the BatchRequest contains a reverse request.
func (ba *BatchRequest) IsReverse() bool {
	return (ba.flags() & isReverse) != 0
}

// IsPossibleTransaction returns true iff the BatchRequest contains
// requests that can be part of a transaction.
func (ba *BatchRequest) IsPossibleTransaction() bool {
	return (ba.flags() & isTxn) != 0
}

// IsTransactionWrite returns true iff the BatchRequest contains a txn write.
func (ba *BatchRequest) IsTransactionWrite() bool {
	return (ba.flags() & isTxnWrite) != 0
}

// IsRange returns true iff the BatchRequest contains a range request.
func (ba *BatchRequest) IsRange() bool {
	return (ba.flags() & isRange) != 0
}

// GetArg returns the first request of the given type, if possible.
func (ba *BatchRequest) GetArg(method Method) (Request, bool) {
	// TODO(tschottdorf): when looking for EndTransaction, just look at the
	// last entry.
	for _, arg := range ba.Requests {
		if req := arg.GetInner(); req.Method() == method {
			return req, true
		}
	}
	return nil, false
}

func (br *BatchResponse) String() string {
	var str []string
	for _, union := range br.Responses {
		str = append(str, fmt.Sprintf("%T", union.GetInner()))
	}
	return strings.Join(str, ", ")
}

// First returns the first response of the given type, if possible.
func (br *BatchResponse) First() Response {
	if len(br.Responses) > 0 {
		return br.Responses[0].GetInner()
	}
	return nil
}

// Header returns a pointer to the header.
func (br *BatchResponse) Header() *BatchResponse_Header {
	return &br.BatchResponse_Header
}

// GetIntents returns a slice of key pairs corresponding to transactional writes
// contained in the batch.
// TODO(tschottdorf): use roachpb.Span here instead of []Intent. Actually
// Intent should be Intents = {Txn, []Span} so that a []Span can
// be turned into Intents easily by just adding a Txn.
func (ba *BatchRequest) GetIntents() []Intent {
	var intents []Intent
	for _, arg := range ba.Requests {
		req := arg.GetInner()
		if !IsTransactionWrite(req) {
			continue
		}
		h := req.Header()
		intents = append(intents, Intent{Key: h.Key, EndKey: h.EndKey})
	}
	return intents
}

// ResetAll resets all the contained requests to their original state.
func (br *BatchResponse) ResetAll() {
	if br == nil {
		return
	}
	for _, rsp := range br.Responses {
		// TODO(tschottdorf) `rsp.Reset()` isn't enough because rsp
		// isn't a pointer.
		rsp.GetInner().Reset()
	}
}

// Combine implements the Combinable interface. It combines each slot of the
// given request into the corresponding slot of the base response. The number
// of slots must be equal and the respective slots must be combinable.
// On error, the receiver BatchResponse is in an invalid state.
// TODO(tschottdorf): write tests.
func (br *BatchResponse) Combine(otherBatch *BatchResponse) error {
	if len(otherBatch.Responses) != len(br.Responses) {
		return errors.New("unable to combine batch responses of different length")
	}
	for i, l := 0, len(br.Responses); i < l; i++ {
		valLeft := br.Responses[i].GetInner()
		valRight := otherBatch.Responses[i].GetInner()
		args, lOK := valLeft.(Combinable)
		reply, rOK := valRight.(Combinable)
		if lOK && rOK {
			if err := args.Combine(reply.(Response)); err != nil {
				return err
			}
			continue
		}
		// If our slot is a NoopResponse, then whatever the other batch has is
		// the result. Note that the result can still be a NoopResponse, to be
		// filled in by a future Combine().
		if _, ok := valLeft.(*NoopResponse); ok {
			br.Responses[i] = otherBatch.Responses[i]
		}
	}
	br.Timestamp.Forward(otherBatch.Timestamp)
	br.Txn.Update(otherBatch.Txn)
	return nil
}

// Add adds a request to the batch request.
func (ba *BatchRequest) Add(requests ...Request) {
	for _, args := range requests {
		union := RequestUnion{}
		if !union.SetValue(args) {
			panic(fmt.Sprintf("unable to add %T to batch request", args))
		}
		ba.Requests = append(ba.Requests, union)
	}
}

// Add adds a response to the batch response.
func (br *BatchResponse) Add(reply Response) {
	union := ResponseUnion{}
	if !union.SetValue(reply) {
		// TODO(tschottdorf) evaluate whether this should return an error.
		panic(fmt.Sprintf("unable to add %T to batch response", reply))
	}
	br.Responses = append(br.Responses, union)
}

// Methods returns a slice of the contained methods.
func (ba *BatchRequest) Methods() []Method {
	var res []Method
	for _, arg := range ba.Requests {
		res = append(res, arg.GetInner().Method())
	}
	return res
}

// CreateReply implements the Request interface. It's slightly different from
// the other implementations: It creates replies for each of the contained
// requests, wrapped in a BatchResponse.
func (ba *BatchRequest) CreateReply() *BatchResponse {
	br := &BatchResponse{}
	for _, union := range ba.Requests {
		br.Add(union.GetInner().CreateReply())
	}
	return br
}

func (ba *BatchRequest) flags() int {
	var flags int
	for _, union := range ba.Requests {
		flags |= union.GetInner().flags()
	}
	return flags
}

// Split separates the requests contained in a batch so that each subset of
// requests can be executed by a Store (without changing order). In particular,
// Admin and EndTransaction requests are always singled out and mutating
// requests separated from reads.
func (ba BatchRequest) Split() [][]RequestUnion {
	compatible := func(exFlags, newFlags int) bool {
		// If no flags are set so far, everything goes.
		if exFlags == 0 {
			return true
		}
		if (newFlags & isAlone) != 0 {
			return false
		}
		// Otherwise, the flags below must remain the same with the new
		// request added.
		//
		// Note that we're not checking isRead: The invariants we're
		// enforcing are that a batch can't mix non-writes with writes.
		// Checking isRead would cause ConditionalPut and Put to conflict,
		// which is not what we want.
		const mask = isWrite | isAdmin | isReverse
		return (mask & exFlags) == (mask & newFlags)
	}
	var parts [][]RequestUnion
	for len(ba.Requests) > 0 {
		part := ba.Requests
		var gFlags int
		for i, union := range ba.Requests {
			flags := union.GetInner().flags()
			if !compatible(gFlags, flags) {
				part = ba.Requests[:i]
				break
			}
			gFlags |= flags
		}
		parts = append(parts, part)
		ba.Requests = ba.Requests[len(part):]
	}
	return parts
}

// String gives a brief summary of the contained requests and keys in the batch.
// TODO(tschottdorf): the key range is useful information, but requires `keys`.
// See #2198.
func (ba BatchRequest) String() string {
	var str []string
	for _, arg := range ba.Requests {
		req := arg.GetInner()
		h := req.Header()
		str = append(str, fmt.Sprintf("%s [%s,%s)", req.Method(), h.Key, h.EndKey))
	}
	return strings.Join(str, ", ")
}

// GetOrCreateCmdID returns the request header's command ID if available.
// Otherwise, creates a new ClientCmdID, initialized with current time
// and random salt.
func (ba BatchRequest) GetOrCreateCmdID(walltime int64) (cmdID ClientCmdID) {
	if !ba.CmdID.IsEmpty() {
		cmdID = ba.CmdID
	} else {
		cmdID = ClientCmdID{
			WallTime: walltime,
			Random:   rand.Int63(),
		}
	}
	return
}

// TraceID implements tracer.Traceable by returning the first nontrivial
// TraceID of the Transaction and CmdID.
func (ba BatchRequest) TraceID() string {
	if r := ba.Txn.TraceID(); r != "" {
		return r
	}
	return ba.CmdID.TraceID()
}

// TraceName implements tracer.Traceable and behaves like TraceID, but using
// the TraceName of the object delegated to.
func (ba BatchRequest) TraceName() string {
	if r := ba.Txn.TraceID(); r != "" {
		return ba.Txn.TraceName()
	}
	return ba.CmdID.TraceName()
}

// TODO(marc): we should assert
// var _ security.RequestWithUser = &BatchRequest{}
// here, but we need to break cycles first.

// GetUser implements security.RequestWithUser.
// KV messages are always sent by the node user.
func (*BatchRequest) GetUser() string {
	// TODO(marc): we should use security.NodeUser here, but we need to break cycles first.
	return "node"
}

// GoError returns the non-nil error from the proto.Error union.
func (br *BatchResponse) GoError() error {
	return br.Error.GoError()
}

// SetGoError converts the specified type into either one of the proto-
// defined error types or into an Error for all other Go errors.
func (br *BatchResponse) SetGoError(err error) {
	if err == nil {
		br.Error = nil
		return
	}
	br.Error = &Error{}
	br.Error.SetGoError(err)
}
