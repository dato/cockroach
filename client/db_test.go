// Copyright 2015 The Cockroach Authors.
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
// Author: Peter Mattis (peter@cockroachlabs.com)

package client_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cockroachdb/cockroach/client"
	"github.com/cockroachdb/cockroach/security"
	"github.com/cockroachdb/cockroach/server"
	"github.com/cockroachdb/cockroach/util/caller"
	"github.com/cockroachdb/cockroach/util/leaktest"
	"github.com/cockroachdb/cockroach/util/log"
)

func setup() (*server.TestServer, *client.DB) {
	s := server.StartTestServer(nil)
	db, err := client.Open(s.Stopper(), fmt.Sprintf("rpcs://%s@%s?certs=test_certs",
		security.NodeUser,
		s.ServingAddr()))
	if err != nil {
		log.Fatal(err)
	}
	return s, db
}

func ExampleDB_Get() {
	s, db := setup()
	defer s.Stop()

	result, err := db.Get("aa")
	if err != nil {
		panic(err)
	}
	fmt.Printf("aa=%s\n", result.ValueBytes())

	// Output:
	// aa=
}

func ExampleDB_Put() {
	s, db := setup()
	defer s.Stop()

	if err := db.Put("aa", "1"); err != nil {
		panic(err)
	}
	result, err := db.Get("aa")
	if err != nil {
		panic(err)
	}
	fmt.Printf("aa=%s\n", result.ValueBytes())

	// Output:
	// aa=1
}

func ExampleDB_CPut() {
	s, db := setup()
	defer s.Stop()

	if err := db.Put("aa", "1"); err != nil {
		panic(err)
	}
	if err := db.CPut("aa", "2", "1"); err != nil {
		panic(err)
	}
	result, err := db.Get("aa")
	if err != nil {
		panic(err)
	}
	fmt.Printf("aa=%s\n", result.ValueBytes())

	if err = db.CPut("aa", "3", "1"); err == nil {
		panic("expected error from conditional put")
	}
	result, err = db.Get("aa")
	if err != nil {
		panic(err)
	}
	fmt.Printf("aa=%s\n", result.ValueBytes())

	if err = db.CPut("bb", "4", "1"); err == nil {
		panic("expected error from conditional put")
	}
	result, err = db.Get("bb")
	if err != nil {
		panic(err)
	}
	fmt.Printf("bb=%s\n", result.ValueBytes())
	if err = db.CPut("bb", "4", nil); err != nil {
		panic(err)
	}
	result, err = db.Get("bb")
	if err != nil {
		panic(err)
	}
	fmt.Printf("bb=%s\n", result.ValueBytes())

	// Output:
	// aa=2
	// aa=2
	// bb=
	// bb=4
}

func ExampleDB_Inc() {
	s, db := setup()
	defer s.Stop()

	if _, err := db.Inc("aa", 100); err != nil {
		panic(err)
	}
	result, err := db.Get("aa")
	if err != nil {
		panic(err)
	}
	fmt.Printf("aa=%d\n", result.ValueInt())

	// Output:
	// aa=100
}

func ExampleBatch() {
	s, db := setup()
	defer s.Stop()

	b := &client.Batch{}
	b.Get("aa")
	b.Put("bb", "2")
	if err := db.Run(b); err != nil {
		panic(err)
	}
	for _, result := range b.Results {
		for _, row := range result.Rows {
			fmt.Printf("%s=%s\n", row.Key, row.ValueBytes())
		}
	}

	// Output:
	// aa=
	// bb=2
}

func ExampleDB_Scan() {
	s, db := setup()
	defer s.Stop()

	b := &client.Batch{}
	b.Put("aa", "1")
	b.Put("ab", "2")
	b.Put("bb", "3")
	if err := db.Run(b); err != nil {
		panic(err)
	}
	rows, err := db.Scan("a", "b", 100)
	if err != nil {
		panic(err)
	}
	for i, row := range rows {
		fmt.Printf("%d: %s=%s\n", i, row.Key, row.ValueBytes())
	}

	// Output:
	// 0: aa=1
	// 1: ab=2
}

func ExampleDB_ReverseScan() {
	s, db := setup()
	defer s.Stop()

	b := &client.Batch{}
	b.Put("aa", "1")
	b.Put("ab", "2")
	b.Put("bb", "3")
	if err := db.Run(b); err != nil {
		panic(err)
	}
	rows, err := db.ReverseScan("ab", "c", 100)
	if err != nil {
		panic(err)
	}
	for i, row := range rows {
		fmt.Printf("%d: %s=%s\n", i, row.Key, row.ValueBytes())
	}

	// Output:
	// 0: bb=3
	// 1: ab=2
}

func ExampleDB_Del() {
	s, db := setup()
	defer s.Stop()

	b := &client.Batch{}
	b.Put("aa", "1")
	b.Put("ab", "2")
	b.Put("ac", "3")
	if err := db.Run(b); err != nil {
		panic(err)
	}
	if err := db.Del("ab"); err != nil {
		panic(err)
	}
	rows, err := db.Scan("a", "b", 100)
	if err != nil {
		panic(err)
	}
	for i, row := range rows {
		fmt.Printf("%d: %s=%s\n", i, row.Key, row.ValueBytes())
	}

	// Output:
	// 0: aa=1
	// 1: ac=3
}

func ExampleTx_Commit() {
	s, db := setup()
	defer s.Stop()

	err := db.Txn(func(txn *client.Txn) error {
		b := &client.Batch{}
		b.Put("aa", "1")
		b.Put("ab", "2")
		return txn.CommitInBatch(b)
	})
	if err != nil {
		panic(err)
	}

	b := &client.Batch{}
	b.Get("aa")
	b.Get("ab")
	if err := db.Run(b); err != nil {
		panic(err)
	}
	for i, result := range b.Results {
		for j, row := range result.Rows {
			fmt.Printf("%d/%d: %s=%s\n", i, j, row.Key, row.ValueBytes())
		}
	}

	// Output:
	// 0/0: aa=1
	// 1/0: ab=2
}

func ExampleDB_Insecure() {
	s := &server.TestServer{}
	s.Ctx = server.NewTestContext()
	s.Ctx.Insecure = true
	if err := s.Start(); err != nil {
		log.Fatalf("Could not start server: %v", err)
	}
	defer s.Stop()

	db, err := client.Open(s.Stopper(), "rpc://foo@"+s.ServingAddr())
	if err != nil {
		log.Fatal(err)
	}

	if err := db.Put("aa", "1"); err != nil {
		panic(err)
	}
	result, err := db.Get("aa")
	if err != nil {
		panic(err)
	}
	fmt.Printf("aa=%s\n", result.ValueBytes())

	// Output:
	// aa=1
}

func TestOpenArgs(t *testing.T) {
	defer leaktest.AfterTest(t)
	s := server.StartTestServer(t)
	defer s.Stop()

	testCases := []struct {
		addr      string
		expectErr bool
	}{
		{"rpcs://" + server.TestUser + "@" + s.ServingAddr() + "?certs=test_certs", false},
		{"rpcs://" + s.ServingAddr() + "?certs=test_certs", false},
		{"rpcs://" + s.ServingAddr() + "?certs=foo", true},
		{s.ServingAddr(), true},
	}

	for _, test := range testCases {
		_, err := client.Open(s.Stopper(), test.addr)
		if test.expectErr && err == nil {
			t.Errorf("Open(%q): expected an error; got %v", test.addr, err)
		} else if !test.expectErr && err != nil {
			t.Errorf("Open(%q): expected no errors; got %v", test.addr, err)
		}
	}
}

func TestDebugName(t *testing.T) {
	defer leaktest.AfterTest(t)
	s, db := setup()
	defer s.Stop()

	file, _, _ := caller.Lookup(0)
	_ = db.Txn(func(txn *client.Txn) error {
		if !strings.HasPrefix(txn.DebugName(), file+":") {
			t.Fatalf("expected \"%s\" to have the prefix \"%s:\"", txn.DebugName(), file)
		}
		return nil
	})
}

func TestCommonMethods(t *testing.T) {
	defer leaktest.AfterTest(t)
	batchType := reflect.TypeOf(&client.Batch{})
	dbType := reflect.TypeOf(&client.DB{})
	txnType := reflect.TypeOf(&client.Txn{})
	types := []reflect.Type{batchType, dbType, txnType}

	type key struct {
		typ    reflect.Type
		method string
	}
	blacklist := map[key]struct{}{
		// TODO(tschottdorf): removed GetProto from Batch, which necessitates
		// these two exceptions. Batch.GetProto would require wrapping each
		// request with the information that this particular Get must be
		// unmarshaled, which didn't seem worth doing as we're not using
		// Batch.GetProto at the moment.
		key{dbType, "GetProto"}:  {},
		key{txnType, "GetProto"}: {},

		key{batchType, "InternalAddRequest"}:      {},
		key{dbType, "AdminMerge"}:                 {},
		key{dbType, "AdminSplit"}:                 {},
		key{dbType, "NewBatch"}:                   {},
		key{dbType, "Run"}:                        {},
		key{dbType, "RunWithResponse"}:            {},
		key{dbType, "Txn"}:                        {},
		key{dbType, "GetSender"}:                  {},
		key{txnType, "Commit"}:                    {},
		key{txnType, "CommitBy"}:                  {},
		key{txnType, "CommitInBatch"}:             {},
		key{txnType, "CommitInBatchWithResponse"}: {},
		key{txnType, "CommitNoCleanup"}:           {},
		key{txnType, "Rollback"}:                  {},
		key{txnType, "Cleanup"}:                   {},
		key{txnType, "DebugName"}:                 {},
		key{txnType, "InternalSetPriority"}:       {},
		key{txnType, "NewBatch"}:                  {},
		key{txnType, "Run"}:                       {},
		key{txnType, "RunWithResponse"}:           {},
		key{txnType, "SetDebugName"}:              {},
		key{txnType, "SetIsolation"}:              {},
		key{txnType, "SetSystemDBTrigger"}:        {},
		key{txnType, "SystemDBTrigger"}:           {},
	}

	for b := range blacklist {
		if _, ok := b.typ.MethodByName(b.method); !ok {
			t.Fatalf("blacklist method (%s).%s does not exist", b.typ, b.method)
		}
	}

	for _, typ := range types {
		for j := 0; j < typ.NumMethod(); j++ {
			m := typ.Method(j)
			if len(m.PkgPath) > 0 {
				continue
			}
			if _, ok := blacklist[key{typ, m.Name}]; ok {
				continue
			}
			for _, otherTyp := range types {
				if typ == otherTyp {
					continue
				}
				if _, ok := otherTyp.MethodByName(m.Name); !ok {
					t.Errorf("(%s).%s does not exist, but (%s).%s does",
						otherTyp, m.Name, typ, m.Name)
				}
			}
		}
	}
}
