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
// Author: Spencer Kimball (spencer.kimball@gmail.com)

package storage

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/cockroachdb/cockroach/keys"
	"github.com/cockroachdb/cockroach/roachpb"
	"github.com/cockroachdb/cockroach/storage/engine"
	"github.com/cockroachdb/cockroach/util/leaktest"
)

func fakePrevKey(k []byte) roachpb.Key {
	const maxLen = 100
	length := len(k)

	// When the byte array is empty.
	if length == 0 {
		panic(fmt.Sprint("cannot get the prev key of an empty key"))
	}
	if length > maxLen {
		panic(fmt.Sprintf("test does not support key longer than %d characters: %q", maxLen, k))
	}

	// If the last byte is a 0, then drop it.
	if k[length-1] == 0 {
		return k[0 : length-1]
	}

	// If the last byte isn't 0, subtract one from it and append "\xff"s
	// until the end of the key space.
	return bytes.Join([][]byte{
		k[0 : length-1],
		{k[length-1] - 1},
		bytes.Repeat([]byte{0xff}, maxLen-length),
	}, nil)
}

// createRangeData creates sample range data in all possible areas of
// the key space. Returns a slice of the encoded keys of all created
// data.
func createRangeData(r *Replica, t *testing.T) []roachpb.EncodedKey {
	ts0 := roachpb.ZeroTimestamp
	ts := roachpb.Timestamp{WallTime: 1}
	keyTSs := []struct {
		key roachpb.Key
		ts  roachpb.Timestamp
	}{
		{keys.ResponseCacheKey(r.Desc().RangeID, &roachpb.ClientCmdID{WallTime: 1, Random: 1}), ts0},
		{keys.ResponseCacheKey(r.Desc().RangeID, &roachpb.ClientCmdID{WallTime: 2, Random: 2}), ts0},
		{keys.RaftHardStateKey(r.Desc().RangeID), ts0},
		{keys.RaftLogKey(r.Desc().RangeID, 1), ts0},
		{keys.RaftLogKey(r.Desc().RangeID, 2), ts0},
		{keys.RangeGCMetadataKey(r.Desc().RangeID), ts0},
		{keys.RangeLastVerificationTimestampKey(r.Desc().RangeID), ts0},
		{keys.RangeStatsKey(r.Desc().RangeID), ts0},
		{keys.RangeDescriptorKey(r.Desc().StartKey), ts},
		{keys.TransactionKey(roachpb.Key(r.Desc().StartKey), []byte("1234")), ts0},
		{keys.TransactionKey(roachpb.Key(r.Desc().StartKey.Next()), []byte("5678")), ts0},
		{keys.TransactionKey(fakePrevKey(r.Desc().EndKey), []byte("2468")), ts0},
		// TODO(bdarnell): KeyMin.Next() results in a key in the reserved system-local space.
		// Once we have resolved https://github.com/cockroachdb/cockroach/issues/437,
		// replace this with something that reliably generates the first valid key in the range.
		//{r.Desc().StartKey.Next(), ts},
		// The following line is similar to StartKey.Next() but adds more to the key to
		// avoid falling into the system-local space.
		{append(append([]byte{}, r.Desc().StartKey...), '\x01'), ts},
		{fakePrevKey(r.Desc().EndKey), ts},
	}

	keys := []roachpb.EncodedKey{}
	for _, keyTS := range keyTSs {
		if err := engine.MVCCPut(r.store.Engine(), nil, keyTS.key, keyTS.ts, roachpb.MakeValueFromString("value"), nil); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, engine.MVCCEncodeKey(keyTS.key))
		if !keyTS.ts.Equal(ts0) {
			keys = append(keys, engine.MVCCEncodeVersionKey(keyTS.key, keyTS.ts))
		}
	}
	return keys
}

// TestReplicaDataIterator verifies correct operation of iterator if
// a range contains no data and never has.
func TestReplicaDataIteratorEmptyRange(t *testing.T) {
	defer leaktest.AfterTest(t)
	tc := testContext{
		bootstrapMode: bootstrapRangeOnly,
	}
	tc.Start(t)
	defer tc.Stop()

	// Adjust the range descriptor to avoid existing data such as meta
	// records and config entries during the iteration. This is a rather
	// nasty little hack, but since it's test code, meh.
	newDesc := *tc.rng.Desc()
	newDesc.StartKey = roachpb.RKey("a")
	if err := tc.rng.setDesc(&newDesc); err != nil {
		t.Fatal(err)
	}

	iter := newReplicaDataIterator(tc.rng.Desc(), tc.rng.store.Engine())
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		t.Error("expected empty iteration")
	}
}

// TestReplicaDataIterator creates three ranges {"a"-"b" (pre), "b"-"c"
// (main test range), "c"-"d" (post)} and fills each with data. It
// first verifies the contents of the "b"-"c" range, then deletes it
// and verifies it's empty. Finally, it verifies the pre and post
// ranges still contain the expected data.
func TestReplicaDataIterator(t *testing.T) {
	defer leaktest.AfterTest(t)
	tc := testContext{
		bootstrapMode: bootstrapRangeOnly,
	}
	tc.Start(t)
	defer tc.Stop()

	// See notes in EmptyRange test method for adjustment to descriptor.
	newDesc := *tc.rng.Desc()
	newDesc.StartKey = roachpb.RKey("b")
	newDesc.EndKey = roachpb.RKey("c")
	if err := tc.rng.setDesc(&newDesc); err != nil {
		t.Fatal(err)
	}
	// Create two more ranges, one before the test range and one after.
	preRng := createRange(tc.store, 2, roachpb.RKeyMin, roachpb.RKey("b"))
	if err := tc.store.AddReplicaTest(preRng); err != nil {
		t.Fatal(err)
	}
	postRng := createRange(tc.store, 3, roachpb.RKey("c"), roachpb.RKeyMax)
	if err := tc.store.AddReplicaTest(postRng); err != nil {
		t.Fatal(err)
	}

	// Create range data for all three ranges.
	preKeys := createRangeData(preRng, t)
	curKeys := createRangeData(tc.rng, t)
	postKeys := createRangeData(postRng, t)

	iter := newReplicaDataIterator(tc.rng.Desc(), tc.rng.store.Engine())
	defer iter.Close()
	i := 0
	for ; iter.Valid(); iter.Next() {
		if err := iter.Error(); err != nil {
			t.Fatal(err)
		}
		if i >= len(curKeys) {
			t.Fatal("there are more keys in the iteration than expected")
		}
		if key := iter.Key(); !key.Equal(curKeys[i]) {
			k1, ts1, _, err := engine.MVCCDecodeKey(key)
			if err != nil {
				t.Fatal(err)
			}
			k2, ts2, _, err := engine.MVCCDecodeKey(curKeys[i])
			if err != nil {
				t.Fatal(err)
			}
			t.Errorf("%d: expected %q(%d); got %q(%d)", i, k2, ts2, k1, ts1)
		}
		i++
	}
	if i != len(curKeys) {
		t.Fatal("there are fewer keys in the iteration than expected")
	}

	// Destroy range and verify that its data has been completely cleared.
	if err := tc.rng.Destroy(); err != nil {
		t.Fatal(err)
	}
	iter = newReplicaDataIterator(tc.rng.Desc(), tc.rng.store.Engine())
	defer iter.Close()
	if iter.Valid() {
		// If the range is destroyed, only a tombstone key should be there.
		k1, _, _, err := engine.MVCCDecodeKey(iter.Key())
		if err != nil {
			t.Fatal(err)
		}
		if tombstoneKey := keys.RaftTombstoneKey(tc.rng.Desc().RangeID); !bytes.Equal(k1, tombstoneKey) {
			t.Errorf("expected a tombstone key %q, but found %q", tombstoneKey, k1)
		}

		if iter.Next(); iter.Valid() {
			t.Errorf("expected a destroyed replica to have only a tombstone key, but found more")
		}
	} else {
		t.Errorf("expected a tombstone key, but got an empty iteration")
	}

	// Verify the keys in pre & post ranges.
	for _, test := range []struct {
		r    *Replica
		keys []roachpb.EncodedKey
	}{
		{preRng, preKeys},
		{postRng, postKeys},
	} {
		iter = newReplicaDataIterator(test.r.Desc(), test.r.store.Engine())
		defer iter.Close()
		i = 0
		for ; iter.Valid(); iter.Next() {
			k1, ts1, _, err := engine.MVCCDecodeKey(iter.Key())
			if err != nil {
				t.Fatal(err)
			}
			if bytes.HasPrefix(k1, keys.StatusPrefix) {
				// Some data is written into the system prefix by Store.BootstrapRange,
				// but it is not in our expected key list so skip it.
				// TODO(bdarnell): validate this data instead of skipping it.
				continue
			}
			if key := iter.Key(); !key.Equal(test.keys[i]) {
				k2, ts2, _, err := engine.MVCCDecodeKey(test.keys[i])
				if err != nil {
					t.Fatal(err)
				}
				t.Errorf("%d: key mismatch %q(%d) != %q(%d)", i, k1, ts1, k2, ts2)
			}
			i++
		}
		if i != len(curKeys) {
			t.Fatal("there are fewer keys in the iteration than expected")
		}
	}
}
