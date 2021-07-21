// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package colmem_test

import (
	"context"
	"math"
	"testing"

	"github.com/cockroachdb/cockroach/pkg/col/coldata"
	"github.com/cockroachdb/cockroach/pkg/col/coldataext"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql/colmem"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfra"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/testutils/skip"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/stretchr/testify/require"
)

func TestResetMaybeReallocate(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	ctx := context.Background()
	st := cluster.MakeTestingClusterSettings()
	testMemMonitor := execinfra.NewTestMemMonitor(ctx, st)
	defer testMemMonitor.Stop(ctx)
	memAcc := testMemMonitor.MakeBoundAccount()
	defer memAcc.Close(ctx)
	evalCtx := tree.MakeTestingEvalContext(st)
	testColumnFactory := coldataext.NewExtendedColumnFactory(&evalCtx)
	testAllocator := colmem.NewAllocator(ctx, &memAcc, testColumnFactory)

	t.Run("ResettingBehavior", func(t *testing.T) {
		if coldata.BatchSize() == 1 {
			skip.IgnoreLint(t, "the test assumes coldata.BatchSize() is at least 2")
		}

		var b coldata.Batch
		typs := []*types.T{types.Bytes}

		// Allocate a new batch and modify it.
		b, _ = testAllocator.ResetMaybeReallocate(typs, b, coldata.BatchSize(), math.MaxInt64)
		b.SetSelection(true)
		b.Selection()[0] = 1
		b.ColVec(0).Bytes().Set(1, []byte("foo"))

		oldBatch := b
		b, _ = testAllocator.ResetMaybeReallocate(typs, b, coldata.BatchSize(), math.MaxInt64)
		// We should have used the same batch, and now it should be in a "reset"
		// state.
		require.Equal(t, oldBatch, b)
		require.Nil(t, b.Selection())
		// We should be able to set in the Bytes vector using an arbitrary
		// position since the vector should have been reset.
		require.NotPanics(t, func() { b.ColVec(0).Bytes().Set(0, []byte("bar")) })
	})

	t.Run("LimitingByMemSize", func(t *testing.T) {
		if coldata.BatchSize() == 1 {
			skip.IgnoreLint(t, "the test assumes coldata.BatchSize() is at least 2")
		}

		var b coldata.Batch
		typs := []*types.T{types.Int}
		const minCapacity = 2
		const maxBatchMemSize = 0

		// Allocate a batch with smaller capacity.
		smallBatch := testAllocator.NewMemBatchWithFixedCapacity(typs, minCapacity/2)

		// Allocate a new batch attempting to use the batch with too small of a
		// capacity - new batch should be allocated.
		b, _ = testAllocator.ResetMaybeReallocate(typs, smallBatch, minCapacity, maxBatchMemSize)
		require.NotEqual(t, smallBatch, b)
		require.Equal(t, minCapacity, b.Capacity())

		oldBatch := b

		// Reset the batch and confirm that a new batch is not allocated because
		// the old batch has enough capacity and it has reached the memory
		// limit.
		b, _ = testAllocator.ResetMaybeReallocate(typs, b, minCapacity, maxBatchMemSize)
		require.Equal(t, oldBatch, b)
		require.Equal(t, minCapacity, b.Capacity())

		if coldata.BatchSize() >= minCapacity*2 {
			// Now reset the batch with large memory limit - we should get a new
			// batch with the double capacity.
			//
			// ResetMaybeReallocate truncates the capacity at
			// coldata.BatchSize(), so we run this part of the test only when
			// doubled capacity will not be truncated.
			b, _ = testAllocator.ResetMaybeReallocate(typs, b, minCapacity, math.MaxInt64)
			require.NotEqual(t, oldBatch, b)
			require.Equal(t, 2*minCapacity, b.Capacity())
		}
	})
}
